// capi/search.go
//go:build cgo

// Search across a store, as its own handle with its own notifier.
//
// A search over a large store is seconds, not minutes, but it is still long
// enough that the GUI thread must not wait on it, and short enough that a row
// model pulled by sequence would be ceremony. So the shape is simpler than a
// run's: progress while it goes (done / total artifacts), then the whole
// result once, sorted and capped by storesearch. Hits are capped at the
// request's limit (storesearch.DefaultLimit when absent), and the result says
// so when the cap was reached.
//
// The contract is ../include/omegacat/search.h.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/scottpeterman/omegacatqt/internal/storesearch"
)

type searchState struct {
	cancel context.CancelFunc
	notify *notifier

	notifyMu   sync.Mutex
	notifyGone bool

	done  atomic.Int64
	total atomic.Int64

	mu       sync.Mutex
	finished bool
	state    string // running, done, cancelled, failed
	errText  string
	result   storesearch.Result
}

func (s *searchState) poke() {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if s.notifyGone || s.notify == nil {
		return
	}
	s.notify.wake()
}

var (
	searchesMu sync.Mutex
	searches   = map[int64]*searchState{}
	nextSearch int64
)

func lookupSearch(h C.longlong) *searchState {
	searchesMu.Lock()
	defer searchesMu.Unlock()
	return searches[int64(h)]
}

type searchRequest struct {
	Query         string   `json:"query"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
	Types         []string `json:"types,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

//export omegacat_search_open
func omegacat_search_open(storeHandle C.longlong, reqJSON *C.char) C.longlong {
	store := lookupStore(storeHandle)
	if store == nil {
		setErr("search: no store %d", int64(storeHandle))
		return -1
	}
	var req searchRequest
	if err := json.Unmarshal([]byte(C.GoString(reqJSON)), &req); err != nil {
		setErr("search request: %v", err)
		return -1
	}
	matcher, err := storesearch.NewLiteral(req.Query, req.CaseSensitive)
	if err != nil {
		setErr("search: %v", err)
		return -1
	}
	n, err := newNotifier()
	if err != nil {
		setErr("search: notifier: %v", err)
		return -1
	}

	ctx, cancel := context.WithCancel(context.Background())
	st := &searchState{cancel: cancel, notify: n, state: "running"}

	go func() {
		res, err := storesearch.Search(ctx, store, matcher, storesearch.Options{
			Types: req.Types,
			Limit: req.Limit,
			OnProgress: func(done, total int) {
				st.done.Store(int64(done))
				st.total.Store(int64(total))
				st.poke()
			},
		})
		st.mu.Lock()
		st.result = res
		st.finished = true
		switch {
		case ctx.Err() != nil:
			st.state = "cancelled"
		case err != nil:
			st.state, st.errText = "failed", err.Error()
		default:
			st.state = "done"
		}
		st.mu.Unlock()
		st.poke()
	}()

	searchesMu.Lock()
	nextSearch++
	h := nextSearch
	searches[h] = st
	searchesMu.Unlock()
	clearErr()
	return C.longlong(h)
}

//export omegacat_search_notify_handle
func omegacat_search_notify_handle(h C.longlong) C.longlong {
	st := lookupSearch(h)
	if st == nil {
		setErr("search_notify_handle: no search %d", int64(h))
		return -1
	}
	clearErr()
	return C.longlong(st.notify.handle())
}

//export omegacat_search_progress
func omegacat_search_progress(h C.longlong) *C.char {
	st := lookupSearch(h)
	if st == nil {
		setErr("search_progress: no search %d", int64(h))
		return nil
	}
	st.mu.Lock()
	out := struct {
		Done     int64  `json:"done"`
		Total    int64  `json:"total"`
		Finished bool   `json:"finished"`
		State    string `json:"state"`
		Error    string `json:"error,omitempty"`
	}{st.done.Load(), st.total.Load(), st.finished, st.state, st.errText}
	st.mu.Unlock()
	return jsonOut("search_progress", out)
}

type wireHit struct {
	Device    string `json:"device"`
	Type      string `json:"type"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Indent    int    `json:"indent"`
}

type wireSkip struct {
	Device string `json:"device"`
	Type   string `json:"type,omitempty"`
	File   string `json:"file,omitempty"`
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

//export omegacat_search_result
func omegacat_search_result(h C.longlong) *C.char {
	st := lookupSearch(h)
	if st == nil {
		setErr("search_result: no search %d", int64(h))
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.finished {
		setErr("search_result: search %d has not finished", int64(h))
		return nil
	}
	r := st.result
	out := struct {
		State     string     `json:"state"`
		Error     string     `json:"error,omitempty"`
		Hits      []wireHit  `json:"hits"`
		Capped    bool       `json:"capped"`
		Limit     int        `json:"limit"`
		Devices   int        `json:"devices"`
		Artifacts int        `json:"artifacts"`
		Bytes     int64      `json:"bytes"`
		Skips     []wireSkip `json:"skips"`
		Warning   string     `json:"warning,omitempty"`
		ElapsedMS int64      `json:"elapsed_ms"`
		Summary   string     `json:"summary"`
	}{
		State: st.state, Error: st.errText,
		Hits:   make([]wireHit, 0, len(r.Hits)),
		Capped: r.Capped, Limit: r.Limit, Devices: r.Devices, Artifacts: r.Artifacts,
		Bytes: r.Bytes, Skips: make([]wireSkip, 0, len(r.Skips)),
		ElapsedMS: ms(r.Elapsed), Summary: r.Summary(),
	}
	for _, hit := range r.Hits {
		out.Hits = append(out.Hits, wireHit{
			Device: hit.Device, Type: hit.Type, File: hit.File, Line: hit.Line,
			Text: hit.Text, Truncated: hit.Truncated, Indent: hit.Indent,
		})
	}
	for _, sk := range r.Skips {
		out.Skips = append(out.Skips, wireSkip{
			Device: sk.Device, Type: sk.Type, File: sk.File, Reason: string(sk.Reason), Error: sk.Err,
		})
	}
	if r.Warning != nil {
		out.Warning = r.Warning.Error()
	}
	return jsonOut("search_result", out)
}

//export omegacat_search_cancel
func omegacat_search_cancel(h C.longlong) C.int {
	st := lookupSearch(h)
	if st == nil {
		setErr("search_cancel: no search %d", int64(h))
		return -1
	}
	st.cancel()
	clearErr()
	return 0
}

//export omegacat_search_close
func omegacat_search_close(h C.longlong) C.int {
	searchesMu.Lock()
	st := searches[int64(h)]
	delete(searches, int64(h))
	searchesMu.Unlock()
	if st == nil {
		setErr("search_close: no search %d", int64(h))
		return -1
	}
	st.cancel()
	st.notifyMu.Lock()
	st.notifyGone = true
	st.notify.close()
	st.notifyMu.Unlock()
	clearErr()
	return 0
}
