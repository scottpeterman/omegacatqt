// internal/capture/browse_test.go
//
// The read surface, tested the way a store view will use it: list devices,
// list a device's types, walk one type's history, open a file.
//
// The assertions worth the most here are the two that pin behaviour a silent
// implementation would get wrong in the same direction — a damaged device
// directory has to be reported rather than skipped, and an unchanged capture
// has to keep pointing at the file whose content matched. Both are cases where
// doing nothing looks exactly like working.
package capture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func browseStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func TestDevicesReportsDirectoriesItCannotIdentify(t *testing.T) {
	s := browseStore(t)
	if _, err := s.Put(dev("lab-r1.lab.example"), "running-config",
		"show running-config", t0, []byte("hostname lab-r1\n"), 0); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A directory that exists but has no device.json — a half-written
	// store, or one somebody edited by hand.
	orphan := filepath.Join(s.Root(), "devices", "lab-r9")
	if err := os.MkdirAll(filepath.Join(orphan, "running-config"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := s.Devices()
	if len(got) != 1 || got[0].Canonical != "lab-r1.lab.example" {
		t.Fatalf("Devices() = %v, want the one readable device", got)
	}

	var broken UnreadableDevices
	if !errors.As(err, &broken) {
		t.Fatalf("Devices() error = %v, want UnreadableDevices so a view can say which directory is damaged", err)
	}
	if len(broken) != 1 || broken[0] != "lab-r9" {
		t.Errorf("unreadable = %v, want [lab-r9]", broken)
	}
}

func TestTypesCountsAttemptsAndStoredSeparately(t *testing.T) {
	s := browseStore(t)
	d := dev("lab-r1.lab.example")

	// Three nightly captures, only the first of which changes anything —
	// the shape of a healthy schedule, and the case where counting files
	// would report a device as barely captured.
	for i, at := range []time.Time{t0, t0.Add(24 * time.Hour), t0.Add(48 * time.Hour)} {
		if _, err := s.Put(d, "running-config", "show running-config", at, []byte("hostname lab-r1\n"), 0); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if _, err := s.Put(d, "inventory", "show inventory", t0.Add(72*time.Hour), []byte("chassis\n"), 0); err != nil {
		t.Fatalf("put inventory: %v", err)
	}

	types, err := s.Types("lab-r1.lab.example")
	if err != nil {
		t.Fatalf("types: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("Types() = %v, want two", types)
	}
	// Newest activity first: inventory ran last.
	if types[0].Type != "inventory" {
		t.Errorf("first type = %q, want inventory (most recent activity)", types[0].Type)
	}

	var rc TypeInfo
	for _, ti := range types {
		if ti.Type == "running-config" {
			rc = ti
		}
	}
	if rc.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3 — every attempt is recorded, including unchanged ones", rc.Attempts)
	}
	if rc.Stored != 1 {
		t.Errorf("Stored = %d, want 1 — only the first capture wrote a file", rc.Stored)
	}
	if !rc.Last.Equal(t0.Add(48 * time.Hour)) {
		t.Errorf("Last = %v, want the newest attempt, not the newest file", rc.Last)
	}
	if rc.File == "" {
		t.Error("File is empty; a type whose latest attempt was unchanged still has a newest stored file")
	}
}

func TestReadReturnsTheContentTheHistoryNames(t *testing.T) {
	s := browseStore(t)
	d := dev("lab-r1.lab.example")
	want := "hostname lab-r1\n!\ninterface Loopback0\n"
	if _, err := s.Put(d, "running-config", "show running-config", t0, []byte(want), 0); err != nil {
		t.Fatalf("put: %v", err)
	}

	hist, err := s.History("lab-r1.lab.example", "running-config")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history = %v, want one entry", hist)
	}

	got, err := s.Read("lab-r1.lab.example", "running-config", hist[0].File)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != want {
		t.Errorf("Read() = %q, want %q", got, want)
	}
}

func TestReadRefusesPathElementsThatAreNotOne(t *testing.T) {
	s := browseStore(t)
	if _, err := s.Put(dev("lab-r1.lab.example"), "running-config",
		"show running-config", t0, []byte("x\n"), 0); err != nil {
		t.Fatalf("put: %v", err)
	}
	// A view is handed these strings; nothing downstream should be able to
	// walk out of the store because one of them was not what it claimed.
	for _, c := range []struct{ typ, file string }{
		{"../..", "device.json"},
		{"running-config", "../device.json"},
		{"running-config", "sub/dir.txt"},
		{"", "x.txt"},
	} {
		if _, err := s.Read("lab-r1.lab.example", c.typ, c.file); err == nil {
			t.Errorf("Read(%q, %q) succeeded; want an error", c.typ, c.file)
		}
	}
	if _, err := s.History("lab-r1.lab.example", "../.."); err == nil {
		t.Error("History with a traversing type succeeded; want an error")
	}
}

func TestBrowsingAnEmptyStoreIsNotAnError(t *testing.T) {
	s := browseStore(t)
	// Opening the store tab before anything has been captured is the first
	// thing that will happen to it.
	devs, err := s.Devices()
	if err != nil || len(devs) != 0 {
		t.Errorf("Devices() = %v, %v; want empty and no error", devs, err)
	}
	types, err := s.Types("lab-r1.lab.example")
	if err != nil || len(types) != 0 {
		t.Errorf("Types() = %v, %v; want empty and no error for a device the store never saw", types, err)
	}
}

// The device list shows "last captured" from device.json alone, without
// parsing any history. That only works because Put merges device info BEFORE
// the dedup branch, so LastSeen moves on an unchanged capture too.
//
// If that ever changes, the symptom is a list where every device on a healthy
// nightly schedule reads as last captured on the day its config last changed —
// months stale, and wrong in the direction that looks like a broken schedule.
func TestLastSeenMovesOnAnUnchangedCapture(t *testing.T) {
	s := browseStore(t)
	d := dev("lab-r1.lab.example")
	content := []byte("hostname lab-r1\n")

	if _, err := s.Put(d, "running-config", "show running-config", t0, content, 0); err != nil {
		t.Fatalf("first put: %v", err)
	}
	later := t0.Add(72 * time.Hour)
	art, err := s.Put(d, "running-config", "show running-config", later, content, 0)
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if !art.Unchanged {
		t.Fatal("the second capture wrote a file; the test is not exercising the unchanged path")
	}

	devs, err := s.Devices()
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want 1", len(devs))
	}
	if !devs[0].LastSeen.Equal(later) {
		t.Errorf("LastSeen = %v, want %v — the device list would report a healthy "+
			"device as stale by the age of its last config change",
			devs[0].LastSeen, later)
	}
	if devs[0].FirstSeen.Equal(later) {
		t.Error("FirstSeen moved with the capture; it should stay at the first one")
	}
}
