// internal/capture/browse.go
//
// The read side of the store.
//
// Store is what the engine writes through: Put and History, and nothing else,
// because an engine that can enumerate a store is an engine that can be asked
// to do something other than capture. Browsing is a separate interface for the
// same reason the engine never imports a toolkit — the two have different
// consumers and want to stay independently implementable. A remote or
// encrypted store is a different Browser and the same Engine.
//
// # Why the view does not build its own paths
//
// Everything here is keyed on the canonical device name and the capture type,
// never on a slug or a path. Slug is exported, so a view could join a root, a
// slug and a type itself and read the file directly — and would then hold a
// second opinion about the layout, which stays correct exactly until the day
// the layout changes. Read exists so that opinion lives in one place.
package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TypeInfo is what the store holds for one (device, capture type).
//
// The counts and timestamps come from history.jsonl, which records every
// attempt including the ones that wrote no file. A list that showed only files
// would report a device captured nightly for a year as having four captures,
// which is true of the disk and misleading about the device.
type TypeInfo struct {
	Type string

	// Attempts is every recorded capture, including unchanged ones.
	Attempts int
	// Stored is the attempts that wrote a new file — the number of
	// distinct versions on disk.
	Stored int

	// Last is the most recent attempt of any kind. On a healthy schedule
	// this moves nightly while Stored does not move at all, and that gap
	// is the useful thing to see: a device whose Last is a month old is
	// not being captured, whatever its file count says.
	Last time.Time
	// Bytes and SHA describe the newest stored file.
	Bytes int
	SHA   string
	// File is the newest stored file's name, for Read.
	File string
}

// Browser is the read surface over a store. It is what a store view is written
// against, so that browsing does not require the concrete FileStore.
type Browser interface {
	// Devices lists what the store holds. A partial list with a non-nil
	// UnreadableDevices error is a normal result, not a failure — see the
	// method comment on FileStore.Devices.
	Devices() ([]DeviceInfo, error)
	// Types lists the capture types held for one device, newest activity
	// first.
	Types(canonical string) ([]TypeInfo, error)
	// History returns the recorded attempts for a device and type, oldest
	// first.
	History(canonical, typ string) ([]HistoryEntry, error)
	// Read returns the content of one stored file, named by a
	// HistoryEntry.File or a TypeInfo.File.
	Read(canonical, typ, file string) ([]byte, error)
}

var _ Browser = (*FileStore)(nil)

// UnreadableDevices names device directories that exist but could not be
// identified, because device.json is missing or unparseable.
//
// It is an error rather than a silent skip. Skipping was the original
// behaviour and it is fine while nothing reads the store: the engine only ever
// writes. In a browser it is a directory full of captures that does not appear
// in the list, with nothing on screen to say why — and the most likely cause
// is a half-written store or a hand-edited one, which is exactly when someone
// is looking.
//
// It is returned ALONGSIDE the devices that did read, because a browser that
// shows nothing because one directory is damaged is worse than one that shows
// the rest and says so.
type UnreadableDevices []string

func (u UnreadableDevices) Error() string {
	return fmt.Sprintf("capture: %d device director%s could not be identified: %s",
		len(u), plural(len(u)), strings.Join(u, ", "))
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// element guards a path component that came from outside.
//
// Canonical names go through Slug, which cannot produce a separator. Capture
// types do not: they reach the store as plain strings, and a browser passes
// back whatever a caller hands it. One check is cheaper than reasoning about
// every path that reaches this file.
func element(kind, name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", fmt.Errorf("capture: empty %s", kind)
	}
	if n != filepath.Base(n) || n == "." || n == ".." || strings.ContainsAny(n, `/\`) {
		return "", fmt.Errorf("capture: %s %q is not a single path element", kind, name)
	}
	return n, nil
}

// deviceDir resolves a canonical name to its directory.
func (s *FileStore) deviceDir(canonical string) (string, error) {
	slug, err := Slug(canonical)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, "devices", slug), nil
}

// Devices lists the devices the store holds, sorted by canonical name.
//
// The error may be non-nil with a usable list; see UnreadableDevices.
func (s *FileStore) Devices() ([]DeviceInfo, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "devices"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var (
		out    []DeviceInfo
		broken UnreadableDevices
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := readDeviceInfo(filepath.Join(s.root, "devices", e.Name()))
		if err != nil || info == nil {
			broken = append(broken, e.Name())
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Canonical < out[j].Canonical })
	if len(broken) > 0 {
		return out, broken
	}
	return out, nil
}

// Types lists the capture types held for one device.
//
// Ordered by most recent activity, because that is the order a device's
// captures are asked about: what ran last night, then everything else.
func (s *FileStore) Types(canonical string) ([]TypeInfo, error) {
	dir, err := s.deviceDir(canonical)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []TypeInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		hist, err := readHistory(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if len(hist) == 0 {
			continue
		}
		ti := TypeInfo{Type: e.Name(), Attempts: len(hist)}
		for _, h := range hist {
			if h.At.After(ti.Last) {
				ti.Last = h.At
			}
			if h.Unchanged {
				continue
			}
			ti.Stored++
			// History is append-only and written in order, so the
			// last non-unchanged line is the newest file.
			ti.Bytes, ti.SHA, ti.File = h.Bytes, h.SHA256, h.File
		}
		out = append(out, ti)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}

// Read returns the content of one stored capture file.
func (s *FileStore) Read(canonical, typ, file string) ([]byte, error) {
	dir, err := s.deviceDir(canonical)
	if err != nil {
		return nil, err
	}
	t, err := element("capture type", typ)
	if err != nil {
		return nil, err
	}
	f, err := element("capture file", file)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, t, f))
}
