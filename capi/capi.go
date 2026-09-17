// capi/capi.go
//go:build cgo

// The C surface over the capture run model, built as a c-archive for the Qt
// application. The contract is include/omegacat/omegacat.h; the header cgo
// writes beside the archive is an artifact.
//
// Same delivery model as omegamaps, for the same reason. A run is a handle,
// and everything the Qt side shows is pulled: progress in one read, rows and
// decisions changed since a sequence number. What tells the Qt side to pull is
// a notifier -- the read end of a self-pipe (a socket pair on Windows) that
// QSocketNotifier watches. A callback into C would fire on a Go-created thread,
// and forgetting to marshal it once is a rare crash rather than a compile
// error. One wake covers any number of changes, so the pipe cannot back up
// behind a fast run.
//
// Results cross as JSON: a pull is one allocation on each side, against a
// struct layout mirrored in two languages that drifts the first time a field
// is added on one side only.
//
// Four handle namespaces, deliberately not shared: runs (this file), stores
// (store.go), searches (search.go) and vaults (vault.go). A store handle passed
// to omegacat_progress is a bug that gets caught instead of a read of the
// wrong thing.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/scottpeterman/omegacatqt/internal/buildinfo"
	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturedial"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
)

func main() {}

// runState is one open run: a live capture or the demo.
type runState struct {
	run    *capturerun.Run
	cancel context.CancelFunc
	notify *notifier

	// notifyMu guards a wake against the notifier being closed underneath
	// it: an OnChange hook captured before close can still fire after it.
	notifyMu   sync.Mutex
	notifyGone bool

	// res is how the run ended and where its output went. Written by the
	// run's own goroutine, so it has its own lock.
	resMu sync.Mutex
	res   runResult
}

// runResult is omegacat_run_result's shape.
type runResult struct {
	Kind      string            `json:"kind"`  // "capture" or "demo"
	State     string            `json:"state"` // running, done, cancelled, failed
	Error     string            `json:"error,omitempty"`
	StorePath string            `json:"store_path,omitempty"`
	LogPath   string            `json:"log_path,omitempty"`
	Devices   int               `json:"devices"`
	Types     []string          `json:"types"`
	Skipped   []string          `json:"skipped"`
	Notes     map[string]string `json:"notes"`
}

func (st *runState) setResult(f func(*runResult)) {
	st.resMu.Lock()
	f(&st.res)
	st.resMu.Unlock()
}

func (st *runState) result() runResult {
	st.resMu.Lock()
	defer st.resMu.Unlock()
	r := st.res
	if r.Types == nil {
		r.Types = []string{}
	}
	if r.Skipped == nil {
		r.Skipped = []string{}
	}
	if r.Notes == nil {
		r.Notes = map[string]string{}
	}
	return r
}

func (st *runState) poke() {
	st.notifyMu.Lock()
	defer st.notifyMu.Unlock()
	if st.notifyGone || st.notify == nil {
		return
	}
	st.notify.wake()
}

var (
	runsMu  sync.Mutex
	runs    = map[int64]*runState{}
	nextRun int64
)

func registerRun(st *runState) C.longlong {
	runsMu.Lock()
	nextRun++
	h := nextRun
	runs[h] = st
	runsMu.Unlock()
	return C.longlong(h)
}

func lookupRun(h C.longlong) *runState {
	runsMu.Lock()
	defer runsMu.Unlock()
	return runs[int64(h)]
}

// newRunState makes the run, its notifier and its cancel, wired together.
func newRunState(kind string) (*runState, context.Context, error) {
	n, err := newNotifier()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &runState{
		run:    capturerun.New(),
		cancel: cancel,
		notify: n,
		res:    runResult{Kind: kind, State: "running"},
	}
	st.run.OnChange(st.poke)
	return st, ctx, nil
}

// cstr hands a Go string to C. The caller frees it with omegacat_free.
func cstr(s string) *C.char { return C.CString(s) }

// jsonOut marshals v for C, or records why it could not.
func jsonOut(what string, v any) *C.char {
	b, err := json.Marshal(v)
	if err != nil {
		setErr("%s: %v", what, err)
		return nil
	}
	clearErr()
	return cstr(string(b))
}

//export omegacat_version
func omegacat_version() *C.char {
	return cstr(buildinfo.String())
}

//export omegacat_free
func omegacat_free(p unsafe.Pointer) {
	C.free(p)
}

// ---------------------------------------------------------------------------
// Capture types

type wireType struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Keep        int      `json:"keep"`
	Expensive   bool     `json:"expensive"`
	Default     bool     `json:"default"`
	Platforms   []string `json:"platforms"`
}

//export omegacat_types
func omegacat_types() *C.char {
	defaults := map[string]bool{}
	for _, t := range capturedial.DefaultTypes {
		defaults[t] = true
	}
	out := []wireType{}
	for _, s := range capture.Builtin() {
		w := wireType{
			Type: s.Type, Description: s.Description, Keep: s.Keep,
			Default: defaults[s.Type], Platforms: s.Platforms(),
		}
		if w.Platforms == nil {
			w.Platforms = []string{}
		}
		for _, c := range s.Commands {
			if c.Cost == capture.CostExpensive {
				w.Expensive = true
			}
		}
		out = append(out, w)
	}
	return jsonOut("types", out)
}

// ---------------------------------------------------------------------------
// Demo

//export omegacat_demo_open
func omegacat_demo_open(stepMS C.int) C.longlong {
	st, ctx, err := newRunState("demo")
	if err != nil {
		setErr("demo: notifier: %v", err)
		return -1
	}
	st.setResult(func(r *runResult) { r.Types = append([]string(nil), capturerun.DemoTypes...) })

	go func() {
		capturerun.Demo(st.run, capturerun.DemoOptions{
			Step: time.Duration(stepMS) * time.Millisecond,
			Stop: ctx.Done(),
		})
		st.setResult(func(r *runResult) {
			if ctx.Err() != nil {
				r.State = "cancelled"
			} else {
				r.State = "done"
			}
		})
		// Last, so a view that sees finished also sees the final state.
		st.run.Finish()
	}()

	h := registerRun(st)
	clearErr()
	return h
}

// ---------------------------------------------------------------------------
// Run lifecycle

//export omegacat_run_result
func omegacat_run_result(h C.longlong) *C.char {
	st := lookupRun(h)
	if st == nil {
		setErr("run_result: no run %d", int64(h))
		return nil
	}
	return jsonOut("run_result", st.result())
}

//export omegacat_notify_handle
func omegacat_notify_handle(h C.longlong) C.longlong {
	st := lookupRun(h)
	if st == nil {
		setErr("notify_handle: no run %d", int64(h))
		return -1
	}
	clearErr()
	return C.longlong(st.notify.handle())
}

//export omegacat_cancel
func omegacat_cancel(h C.longlong) C.int {
	st := lookupRun(h)
	if st == nil {
		setErr("cancel: no run %d", int64(h))
		return -1
	}
	st.cancel()
	clearErr()
	return 0
}

//export omegacat_close
func omegacat_close(h C.longlong) C.int {
	runsMu.Lock()
	st := runs[int64(h)]
	delete(runs, int64(h))
	runsMu.Unlock()
	if st == nil {
		setErr("close: no run %d", int64(h))
		return -1
	}
	st.cancel()
	st.run.OnChange(nil)
	st.notifyMu.Lock()
	st.notifyGone = true
	st.notify.close()
	st.notifyMu.Unlock()
	clearErr()
	return 0
}

// ---------------------------------------------------------------------------
// Wire shapes. snake_case, stable, additive: fields may be added and a reader
// ignores what it does not know.

type wireCounts struct {
	Devices        int `json:"devices"`
	DevicesFailed  int `json:"devices_failed"`
	Stored         int `json:"stored"`
	Unchanged      int `json:"unchanged"`
	NotApplicable  int `json:"not_applicable"`
	Failed         int `json:"failed"`
	Running        int `json:"running"`
	BytesStored    int `json:"bytes_stored"`
	NewHostKeys    int `json:"new_host_keys"`
	CredRejections int `json:"cred_rejections"`
}

// wireRow is one (device, capture type) pair. file is the stored file's name,
// which is what omegacat_store_read takes alongside name and type; path is
// the same file's full path, for "show in folder".
type wireRow struct {
	Seq        uint64 `json:"seq"`
	Identity   string `json:"identity"`
	Name       string `json:"name,omitempty"`
	Display    string `json:"display"`
	Type       string `json:"type"`
	Platform   string `json:"platform,omitempty"`
	State      string `json:"state"`
	Command    string `json:"command,omitempty"`
	Bytes      int    `json:"bytes"`
	SHA        string `json:"sha256,omitempty"`
	Path       string `json:"path,omitempty"`
	File       string `json:"file,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms"`

	ParseStatus  string  `json:"parse_status,omitempty"` // "parsed", "no-match", or absent
	Template     string  `json:"template,omitempty"`
	ParseScore   float64 `json:"parse_score,omitempty"`
	ParseRecords int     `json:"parse_records,omitempty"`
}

type wireProgress struct {
	Seq       uint64     `json:"seq"`
	ElapsedMS int64      `json:"elapsed_ms"`
	Finished  bool       `json:"finished"`
	Total     int        `json:"total"`
	Settled   int        `json:"settled"`
	Counts    wireCounts `json:"counts"`
	Running   []wireRow  `json:"running"`
}

type wireDecision struct {
	Seq      uint64 `json:"seq"`
	AtMS     int64  `json:"at_ms"`
	Kind     string `json:"kind"`
	Identity string `json:"identity,omitempty"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Text     string `json:"text"`
}

func ms(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

func toWireRow(r capturerun.Row) wireRow {
	w := wireRow{
		Seq: r.Seq, Identity: r.Identity, Name: r.Name, Display: r.Display(),
		Type: r.Type, Platform: r.Platform, State: r.State.String(),
		Command: r.Command, Bytes: r.Bytes, SHA: r.SHA, Path: r.Path,
		Detail: r.Detail, DurationMS: ms(r.Duration()),
		ParseStatus: r.ParseStatus, Template: r.Template,
		ParseScore: r.ParseScore, ParseRecords: r.ParseRecords,
	}
	if r.Path != "" {
		w.File = filepath.Base(r.Path)
	}
	return w
}

//export omegacat_progress
func omegacat_progress(h C.longlong) *C.char {
	st := lookupRun(h)
	if st == nil {
		setErr("progress: no run %d", int64(h))
		return nil
	}
	p := st.run.Progress()
	c := p.Counts
	w := wireProgress{
		Seq: p.Seq, ElapsedMS: ms(p.Elapsed), Finished: p.Finished,
		Total: p.Total, Settled: p.Settled,
		Counts: wireCounts{
			Devices: c.Devices, DevicesFailed: c.DevicesFailed, Stored: c.Stored,
			Unchanged: c.Unchanged, NotApplicable: c.NotApplicable, Failed: c.Failed,
			Running: c.Running, BytesStored: c.BytesStored, NewHostKeys: c.NewHostKeys,
			CredRejections: c.CredRejections,
		},
		Running: make([]wireRow, 0, len(p.Running)),
	}
	for _, r := range p.Running {
		w.Running = append(w.Running, toWireRow(r))
	}
	return jsonOut("progress", w)
}

//export omegacat_rows_since
func omegacat_rows_since(h C.longlong, seq C.ulonglong) *C.char {
	st := lookupRun(h)
	if st == nil {
		setErr("rows_since: no run %d", int64(h))
		return nil
	}
	rows, next := st.run.RowsSince(uint64(seq))
	out := struct {
		Seq  uint64    `json:"seq"`
		Rows []wireRow `json:"rows"`
	}{Seq: next, Rows: make([]wireRow, 0, len(rows))}
	for _, r := range rows {
		out.Rows = append(out.Rows, toWireRow(r))
	}
	return jsonOut("rows_since", out)
}

//export omegacat_decisions_since
func omegacat_decisions_since(h C.longlong, seq C.ulonglong) *C.char {
	st := lookupRun(h)
	if st == nil {
		setErr("decisions_since: no run %d", int64(h))
		return nil
	}
	evs, next := st.run.DecisionsSince(uint64(seq))
	out := struct {
		Seq       uint64         `json:"seq"`
		Decisions []wireDecision `json:"decisions"`
	}{Seq: next, Decisions: make([]wireDecision, 0, len(evs))}
	for _, ev := range evs {
		d := wireDecision{
			Seq: ev.Seq, AtMS: ev.At.UnixMilli(), Kind: ev.Kind.String(),
			Identity: ev.Identity, Type: ev.Type, Name: ev.Name,
			Platform: ev.Platform, Detail: ev.Detail, Text: ev.Describe(),
		}
		if d.Detail == "" && ev.Err != nil {
			d.Detail = ev.Err.Error()
		}
		out.Decisions = append(out.Decisions, d)
	}
	return jsonOut("decisions_since", out)
}
