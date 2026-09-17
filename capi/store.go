// capi/store.go
//go:build cgo

// The store, browsable with no run at all.
//
// Opening a store and reading last night's config with nothing capturing is a
// legitimate session and, once captures are scheduled, probably the common
// one. So the store is its own handle rather than something a run owns.
//
// Everything here is a synchronous local file read, safe from any thread. The
// list calls are small. omegacat_store_read returns a whole capture -- up to
// capture.ConfigMaxBytes -- and is the one call worth making off the GUI
// thread for a large config.
//
// The contract is ../include/omegacat/store.h.
package main

/*
#include <stdlib.h>
#include <string.h>
*/
import "C"

import (
	"errors"
	"sync"
	"time"
	"unsafe"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/diffignore"
	"github.com/scottpeterman/omegacatqt/internal/tfsmfire"
)

var (
	storesMu  sync.Mutex
	stores    = map[int64]*capture.FileStore{}
	nextStore int64
)

func lookupStore(h C.longlong) *capture.FileStore {
	storesMu.Lock()
	defer storesMu.Unlock()
	return stores[int64(h)]
}

func unixMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

//export omegacat_store_open
func omegacat_store_open(root *C.char) C.longlong {
	if root == nil {
		setErr("store_open: no path")
		return -1
	}
	s, err := capture.OpenFileStore(C.GoString(root))
	if err != nil {
		setErr("store_open: %v", err)
		return -1
	}
	storesMu.Lock()
	nextStore++
	h := nextStore
	stores[h] = s
	storesMu.Unlock()
	clearErr()
	return C.longlong(h)
}

//export omegacat_store_close
func omegacat_store_close(h C.longlong) C.int {
	storesMu.Lock()
	_, ok := stores[int64(h)]
	delete(stores, int64(h))
	storesMu.Unlock()
	if !ok {
		setErr("store_close: no store %d", int64(h))
		return -1
	}
	clearErr()
	return 0
}

//export omegacat_store_root
func omegacat_store_root(h C.longlong) *C.char {
	s := lookupStore(h)
	if s == nil {
		setErr("store_root: no store %d", int64(h))
		return nil
	}
	clearErr()
	return cstr(s.Root())
}

type wireDevice struct {
	Canonical   string   `json:"canonical"`
	Aliases     []string `json:"aliases"`
	Platform    string   `json:"platform,omitempty"`
	FirstSeenMS int64    `json:"first_seen_ms"`
	LastSeenMS  int64    `json:"last_seen_ms"`
}

//export omegacat_store_devices
func omegacat_store_devices(h C.longlong) *C.char {
	s := lookupStore(h)
	if s == nil {
		setErr("store_devices: no store %d", int64(h))
		return nil
	}
	devs, err := s.Devices()
	out := struct {
		Devices    []wireDevice `json:"devices"`
		Unreadable []string     `json:"unreadable"`
	}{Devices: make([]wireDevice, 0, len(devs)), Unreadable: []string{}}

	// A partial list with UnreadableDevices is a normal result: show the
	// devices that read and name the directories that did not.
	var unreadable capture.UnreadableDevices
	if err != nil {
		if !errors.As(err, &unreadable) {
			setErr("store_devices: %v", err)
			return nil
		}
		out.Unreadable = append(out.Unreadable, unreadable...)
	}
	for _, d := range devs {
		w := wireDevice{
			Canonical: d.Canonical, Aliases: d.Aliases, Platform: d.Platform,
			FirstSeenMS: unixMS(d.FirstSeen), LastSeenMS: unixMS(d.LastSeen),
		}
		if w.Aliases == nil {
			w.Aliases = []string{}
		}
		out.Devices = append(out.Devices, w)
	}
	return jsonOut("store_devices", out)
}

type wireTypeInfo struct {
	Type     string `json:"type"`
	Attempts int    `json:"attempts"`
	Stored   int    `json:"stored"`
	LastMS   int64  `json:"last_ms"`
	Bytes    int    `json:"bytes"`
	SHA      string `json:"sha256,omitempty"`
	File     string `json:"file,omitempty"`
}

//export omegacat_store_types
func omegacat_store_types(h C.longlong, canonical *C.char) *C.char {
	s := lookupStore(h)
	if s == nil {
		setErr("store_types: no store %d", int64(h))
		return nil
	}
	infos, err := s.Types(C.GoString(canonical))
	if err != nil {
		setErr("store_types: %v", err)
		return nil
	}
	out := make([]wireTypeInfo, 0, len(infos))
	for _, t := range infos {
		out = append(out, wireTypeInfo{
			Type: t.Type, Attempts: t.Attempts, Stored: t.Stored, LastMS: unixMS(t.Last),
			Bytes: t.Bytes, SHA: t.SHA, File: t.File,
		})
	}
	return jsonOut("store_types", out)
}

type wireHistory struct {
	AtMS      int64  `json:"at_ms"`
	Command   string `json:"command"`
	SHA       string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	File      string `json:"file"`
	Unchanged bool   `json:"unchanged"`
}

//export omegacat_store_history
func omegacat_store_history(h C.longlong, canonical, typ *C.char) *C.char {
	s := lookupStore(h)
	if s == nil {
		setErr("store_history: no store %d", int64(h))
		return nil
	}
	entries, err := s.History(C.GoString(canonical), C.GoString(typ))
	if err != nil {
		setErr("store_history: %v", err)
		return nil
	}
	out := make([]wireHistory, 0, len(entries))
	for _, e := range entries {
		out = append(out, wireHistory{
			AtMS: unixMS(e.At), Command: e.Command, SHA: e.SHA256, Bytes: e.Bytes,
			File: e.File, Unchanged: e.Unchanged,
		})
	}
	return jsonOut("store_history", out)
}

// omegacat_store_read returns the file's bytes with a NUL after them, and the
// length without it. The length is the answer, not strlen: stored content is
// what the device said, and the NUL is only there so a caller that knows the
// file is text can use it as a C string.
//
//export omegacat_store_read
func omegacat_store_read(h C.longlong, canonical, typ, file *C.char, length *C.longlong) *C.char {
	s := lookupStore(h)
	if s == nil {
		setErr("store_read: no store %d", int64(h))
		return nil
	}
	data, err := s.Read(C.GoString(canonical), C.GoString(typ), C.GoString(file))
	if err != nil {
		setErr("store_read: %v", err)
		return nil
	}
	buf := (*C.char)(C.malloc(C.size_t(len(data) + 1)))
	if buf == nil {
		setErr("store_read: out of memory for %d bytes", len(data))
		return nil
	}
	if len(data) > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&data[0]), C.size_t(len(data)))
	}
	*(*C.char)(unsafe.Add(unsafe.Pointer(buf), len(data))) = 0
	if length != nil {
		*length = C.longlong(len(data))
	}
	clearErr()
	return buf
}

// omegacat_store_parsed returns the parse written beside one stored file, or
// "null" when there is none. Not an error: most types are not parsed, and a
// capture from before parsing existed has no sidecar.
//
//export omegacat_store_parsed
func omegacat_store_parsed(h C.longlong, canonical, typ, file *C.char) *C.char {
	s := lookupStore(h)
	if s == nil {
		setErr("store_parsed: no store %d", int64(h))
		return nil
	}
	pf, ok, err := s.ReadParsed(C.GoString(canonical), C.GoString(typ), C.GoString(file))
	if err != nil {
		setErr("store_parsed: %v", err)
		return nil
	}
	if !ok {
		clearErr()
		return cstr("null")
	}
	return jsonOut("store_parsed", wireParsed{
		Status: pf.Status, Template: pf.Template, Score: pf.Score, Hint: pf.Hint,
		Tried: pf.Tried, Header: nonNil(pf.Header), Records: pf.Records,
		ParsedAtMS: unixMS(pf.ParsedAt), RawFile: pf.RawFile, RawSHA256: pf.RawSHA256,
	})
}

type wireParsed struct {
	Status     string            `json:"status"`
	Template   string            `json:"template,omitempty"`
	Score      float64           `json:"score"`
	Hint       string            `json:"hint"`
	Tried      int               `json:"tried"`
	Header     []string          `json:"header"`
	Records    []tfsmfire.Record `json:"records"`
	ParsedAtMS int64             `json:"parsed_at_ms"`
	RawFile    string            `json:"raw_file"`
	RawSHA256  string            `json:"raw_sha256"`
}

// omegacat_store_diff compares two stored versions of a static capture.
//
//export omegacat_store_diff
func omegacat_store_diff(h C.longlong, canonical, typ, olderFile, newerFile *C.char, context C.int) *C.char {
	s := lookupStore(h)
	if s == nil {
		setErr("store_diff: no store %d", int64(h))
		return nil
	}
	path := diffIgnorePath()
	rules, rulesErr := diffignore.Load(path)
	d, err := s.DiffWith(C.GoString(canonical), C.GoString(typ), C.GoString(olderFile), C.GoString(newerFile),
		int(context), rules.For)
	if err != nil {
		setErr("store_diff: %v", err)
		return nil
	}
	out := wireDiff{Diff: d, RulesPath: path}
	if rulesErr != nil {
		out.RulesWarning = rulesErr.Error()
	}
	return jsonOut("store_diff", out)
}

type wireDiff struct {
	capture.Diff
	RulesPath    string `json:"rules_path"`
	RulesWarning string `json:"rules_warning,omitempty"`
}

var (
	diffIgnoreMu       sync.Mutex
	diffIgnoreOverride string
)

func diffIgnorePath() string {
	diffIgnoreMu.Lock()
	defer diffIgnoreMu.Unlock()
	if diffIgnoreOverride != "" {
		return diffIgnoreOverride
	}
	return diffignore.Path()
}

// omegacat_diff_ignore_path sets the diff ignore rules file when path is not
// NULL or "", and returns the file in use.
//
//export omegacat_diff_ignore_path
func omegacat_diff_ignore_path(path *C.char) *C.char {
	if path != nil {
		if p := C.GoString(path); p != "" {
			diffIgnoreMu.Lock()
			diffIgnoreOverride = p
			diffIgnoreMu.Unlock()
		}
	}
	clearErr()
	return cstr(diffIgnorePath())
}

// omegacat_store_diffable reports whether a capture type gets a line diff.
//
//export omegacat_store_diffable
func omegacat_store_diffable(typ *C.char) C.int {
	if capture.Diffable(C.GoString(typ)) {
		return 1
	}
	return 0
}
