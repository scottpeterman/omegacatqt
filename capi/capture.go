// capi/capture.go
//go:build cgo

// A live capture behind the same handle the demo returns.
//
// The request is JSON: capturerun.Params with durations in milliseconds,
// because C++ has no time.Duration. Credentials are NOT in it. They come from a
// vault handle opened and unlocked through the vault surface and are resolved
// inside Go per device, so no password sits in the request or on the C++ side.
// That is also why a vault is required here when the command line accepts a
// password: the static-credential path would put the secret on the wrong side.
//
// Assembly goes through capturedial.Build, the path cmd/capture takes, so the
// window cannot capture differently from the command line. Build runs on the
// run's goroutine rather than in omegacat_capture_open: it resolves CGNAT
// addresses (reverse and forward DNS) and reads the session file, and the
// header promises nothing here blocks on the network. A request that is wrong
// in shape is refused synchronously by omegacat_capture_validate and by the
// same check at open; a failure Build finds later ends the run as "failed"
// with the reason in omegacat_run_result.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturedial"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/tfsmfire"
	"github.com/scottpeterman/omegacatqt/internal/vault"
)

// captureRequest is the wire form. Pointers where zero is a value a form
// could send by mistake and the default should win instead.
type captureRequest struct {
	Devices     []string `json:"devices"`
	DeviceFile  string   `json:"device_file,omitempty"`
	SessionFile string   `json:"session_file,omitempty"`
	Match       []string `json:"match,omitempty"`
	SessionKeys []string `json:"session_keys,omitempty"`
	Types       []string `json:"types"`
	StorePath   string   `json:"store_path"`

	Concurrency          *int  `json:"concurrency,omitempty"`
	ExpensiveConcurrency *int  `json:"expensive_concurrency,omitempty"`
	TimeoutMS            int64 `json:"timeout_ms,omitempty"`

	Domains        []string `json:"domains,omitempty"`
	CredTags       []string `json:"cred_tags,omitempty"`
	HostKeys       string   `json:"host_keys,omitempty"` // "strict" or "tofu"
	KnownHostsPath string   `json:"known_hosts_path,omitempty"`
	Legacy         bool     `json:"legacy,omitempty"`

	// LogPath is where the text log goes. Empty means
	// <store_path>/logs/capture-<UTC timestamp>.log.
	LogPath string `json:"log_path,omitempty"`

	// NoParse stores ARP and MAC tables without parsing them.
	// TemplatesPath is the template database; empty is the one in the
	// configuration directory.
	NoParse       bool   `json:"no_parse,omitempty"`
	TemplatesPath string `json:"templates_path,omitempty"`
}

// params turns the request into capturerun.Params over the defaults.
func (r captureRequest) params(vaultPath string) capturerun.Params {
	p := capturerun.Defaults()
	// Devices pasted as one blob -- a spreadsheet column, a ticket -- split
	// the way the device-list file does, comments included.
	p.Devices = capturerun.ParseDevices(strings.Join(r.Devices, "\n"))
	p.DeviceFile = r.DeviceFile
	p.SessionFile = r.SessionFile
	p.Match = r.Match
	p.SessionKeys = r.SessionKeys
	p.Types = r.Types
	p.StorePath = r.StorePath
	if r.Concurrency != nil {
		p.Concurrency = *r.Concurrency
	}
	if r.ExpensiveConcurrency != nil {
		p.ExpensiveConcurrency = *r.ExpensiveConcurrency
	}
	if r.TimeoutMS > 0 {
		p.Timeout = time.Duration(r.TimeoutMS) * time.Millisecond
	}
	p.Domains = r.Domains
	p.CredTags = r.CredTags
	if r.HostKeys != "" {
		p.HostKeys = capturerun.HostKeyMode(r.HostKeys)
	}
	p.KnownHostsPath = r.KnownHostsPath
	p.Legacy = r.Legacy
	p.NoParse = r.NoParse
	p.TemplatesPath = r.TemplatesPath
	p.VaultPath = vaultPath
	p.Normalize()
	return p
}

type wireValidation struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// problems is every reason the request cannot run, with field names the
// request's own, so a form can find the widget. vaultPath stands in for the
// vault the run will use: tags are only meaningful with one, and a capture
// through this surface always has one.
func (r captureRequest) problems(vaultPath string) []wireValidation {
	p := r.params(vaultPath)
	out := []wireValidation{}
	for _, e := range p.ValidateAgainst(capturedial.KnownTypes()) {
		field := e.Field
		if field == "timeout" {
			field = "timeout_ms"
		}
		out = append(out, wireValidation{Field: field, Message: e.Message})
	}
	return out
}

//export omegacat_capture_defaults
func omegacat_capture_defaults() *C.char {
	d := capturerun.Defaults()
	conc, exp := d.Concurrency, d.ExpensiveConcurrency
	return jsonOut("capture_defaults", captureRequest{
		Devices:              []string{},
		Types:                append([]string(nil), capturedial.DefaultTypes...),
		Concurrency:          &conc,
		ExpensiveConcurrency: &exp,
		TimeoutMS:            d.Timeout.Milliseconds(),
		HostKeys:             string(d.HostKeys),
	})
}

//export omegacat_capture_validate
func omegacat_capture_validate(reqJSON *C.char) *C.char {
	var req captureRequest
	if err := json.Unmarshal([]byte(C.GoString(reqJSON)), &req); err != nil {
		setErr("capture request: %v", err)
		return nil
	}
	return jsonOut("capture_validate", req.problems("vault"))
}

// captureLog is the run's text log: what to read when a device did something
// the events do not explain. Timestamped, shared by the engine, the
// credential resolver and the host-key announcer.
type captureLog struct {
	mu sync.Mutex
	f  *os.File
}

func (l *captureLog) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	fmt.Fprintf(l.f, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

func (l *captureLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

//export omegacat_capture_open
func omegacat_capture_open(vaultHandle C.longlong, reqJSON *C.char) C.longlong {
	var req captureRequest
	if err := json.Unmarshal([]byte(C.GoString(reqJSON)), &req); err != nil {
		setErr("capture request: %v", err)
		return -1
	}

	if vaultHandle <= 0 {
		setErr("capture: a vault handle is required; credentials never cross this surface")
		return -1
	}
	v := vlookup(vaultHandle)
	if v == nil {
		setErr("capture: no vault handle %d", int64(vaultHandle))
		return -1
	}
	if v.IsLocked() {
		setErr("capture: the vault is locked; unlock it first")
		return -1
	}
	vaultPath := v.Path()

	if probs := req.problems(vaultPath); len(probs) > 0 {
		msgs := make([]string, 0, len(probs))
		for _, pr := range probs {
			msgs = append(msgs, pr.Field+": "+pr.Message)
		}
		setErr("invalid capture request:\n  %s", strings.Join(msgs, "\n  "))
		return -1
	}
	p := req.params(vaultPath)

	logPath := req.LogPath
	if logPath == "" {
		logPath = filepath.Join(p.StorePath, "logs",
			"capture-"+time.Now().UTC().Format(capture.TimeLayout)+".log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		setErr("capture: log directory: %v", err)
		return -1
	}
	lf, err := os.Create(logPath)
	if err != nil {
		setErr("capture: log file: %v", err)
		return -1
	}
	logw := &captureLog{f: lf}

	st, ctx, err := newRunState("capture")
	if err != nil {
		logw.close()
		os.Remove(logPath)
		setErr("capture: notifier: %v", err)
		return -1
	}
	st.setResult(func(r *runResult) { r.StorePath, r.LogPath = p.StorePath, logPath })

	go runCapture(st, ctx, p, v, logw)

	h := registerRun(st)
	clearErr()
	return h
}

// runCapture assembles the engine and runs it. Finish is the last thing that
// happens, so a view that sees "finished" also sees the final result.
func runCapture(st *runState, ctx context.Context, p capturerun.Params, v *vault.Vault, logw *captureLog) {
	defer logw.close()

	opts := capturedial.Options{
		Vault:    v,
		Log:      logw.printf,
		CredLog:  logw.printf,
		Announce: logw.printf,
		Emit:     st.run.Emit(),
	}
	if !p.NoParse {
		specs, err := capturedial.SpecsFor(p.Types)
		if err == nil && capturedial.NeedsParser(specs) {
			eng, err := sharedTemplates(p)
			if err != nil {
				logw.printf("capture: %v", err)
				st.setResult(func(r *runResult) { r.State, r.Error = "failed", err.Error() })
				st.run.Finish()
				return
			}
			opts.Parser = eng
		}
	}

	built, err := capturedial.Build(p, opts)
	if err != nil {
		logw.printf("capture: %v", err)
		st.setResult(func(r *runResult) { r.State, r.Error = "failed", err.Error() })
		st.run.Finish()
		return
	}

	types := make([]string, 0, len(built.Specs))
	for _, s := range built.Specs {
		types = append(types, s.Type)
	}
	st.setResult(func(r *runResult) {
		r.Devices = len(built.Devices)
		r.Types = types
		r.Skipped = capturedial.SkippedLines(built.Skipped)
		r.Notes = built.Notes
	})
	logw.printf("capture: %d device(s) x %d type(s) [%s] -> %s, vault %s",
		len(built.Devices), len(types), strings.Join(types, ", "), built.Store.Root(), p.VaultPath)
	ids := make([]string, 0, len(built.Notes))
	for id := range built.Notes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		logw.printf("capture: %s: %s", id, built.Notes[id])
	}

	results := built.Engine.Capture(ctx, built.Devices)
	built.Close()

	failed := 0
	for _, res := range results {
		if !res.OK() {
			failed++
		}
	}
	logw.printf("capture: %d result(s), %d failed", len(results), failed)

	st.setResult(func(r *runResult) {
		if ctx.Err() != nil {
			r.State = "cancelled"
		} else {
			r.State = "done"
		}
	})
	st.run.Finish()
}

// Template databases open for the life of the process, by path. Opening one
// compiles every template -- seconds -- and a window that runs a capture every
// few minutes should pay that once, not per run. A path that failed to open
// is not cached, so fixing the file and running again works.
//
// The Template Lab shares these (templates.go): a sweep in the Lab uses the
// engine the next capture will, and a save reloads it, so an edited template
// applies to that capture without a restart.
var (
	templatesMu sync.Mutex
	templateDBs = map[string]*tfsmfire.Engine{}
)

func sharedTemplates(p capturerun.Params) (*tfsmfire.Engine, error) {
	path, err := capturedial.TemplatesPath(p)
	if err != nil {
		return nil, err
	}
	eng, err := engineFor(path)
	if err != nil {
		return nil, fmt.Errorf("%w (disable parsing to capture without it)", err)
	}
	return eng, nil
}

// engineFor is the shared engine for a database path, opened on first use.
func engineFor(path string) (*tfsmfire.Engine, error) {
	templatesMu.Lock()
	defer templatesMu.Unlock()
	if eng, ok := templateDBs[path]; ok {
		return eng, nil
	}
	eng, err := tfsmfire.Open(path)
	if err != nil {
		return nil, err
	}
	templateDBs[path] = eng
	return eng, nil
}

// reloadShared re-reads a database into its shared engine, when one is open.
// A database nobody has opened yet needs nothing: its first open reads it.
func reloadShared(path string) error {
	templatesMu.Lock()
	eng, ok := templateDBs[path]
	templatesMu.Unlock()
	if !ok {
		return nil
	}
	return eng.Reload()
}
