// internal/capture/store_test.go
//
// The store's contract, with the dedup path given most of the attention:
// skipping a write is the one behavior here that can lose information, so
// every test below asks whether the record survived the saving.
package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func store(t *testing.T) *FileStore {
	t.Helper()
	s, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func dev(name string) DeviceInfo {
	return DeviceInfo{Canonical: name, Platform: "cisco_ios"}
}

var t0 = time.Date(2026, 7, 31, 15, 4, 22, 0, time.UTC)

func TestFirstCaptureWritesAFileAndAHistoryLine(t *testing.T) {
	s := store(t)
	art, err := s.Put(dev("lab-r1.lab.example"), "running-config", "show running-config", t0, []byte("hostname lab-r1\n"), 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if art.Unchanged {
		t.Error("first capture reported as unchanged")
	}
	if got, err := os.ReadFile(art.Path); err != nil || string(got) != "hostname lab-r1\n" {
		t.Fatalf("stored content = %q, %v", got, err)
	}
	h, err := s.History("lab-r1.lab.example", "running-config")
	if err != nil || len(h) != 1 {
		t.Fatalf("history = %d entries, %v; want 1", len(h), err)
	}
	if h[0].Command != "show running-config" || h[0].Unchanged {
		t.Errorf("history entry wrong: %+v", h[0])
	}
}

// The saving, and the reason it is safe. An identical capture must not
// produce a second file, and must still produce a history line — otherwise
// "nothing changed" and "we never ran" are the same thing on disk, which is
// the wrong trade for a backup.
func TestIdenticalCaptureWritesNoFileButStillRecordsTheAttempt(t *testing.T) {
	s := store(t)
	body := []byte("hostname lab-r1\n")
	first, _ := s.Put(dev("lab-r1"), "running-config", "show running-config", t0, body, 0)
	second, err := s.Put(dev("lab-r1"), "running-config", "show running-config", t0.Add(time.Hour), body, 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	if !second.Unchanged {
		t.Error("identical capture was not reported as unchanged")
	}
	if second.Path != first.Path {
		t.Errorf("unchanged capture points at %q, want the existing file %q", second.Path, first.Path)
	}
	files := captureFiles(t, filepath.Dir(first.Path))
	if len(files) != 1 {
		t.Errorf("identical capture wrote a second file: %v", files)
	}
	h, _ := s.History("lab-r1", "running-config")
	if len(h) != 2 {
		t.Fatalf("history = %d entries, want 2 — the skipped write must still be recorded", len(h))
	}
	if !h[1].Unchanged || h[1].File != filepath.Base(first.Path) {
		t.Errorf("second entry does not point back at the matching file: %+v", h[1])
	}
}

func TestChangedCaptureWritesANewFile(t *testing.T) {
	s := store(t)
	first, _ := s.Put(dev("lab-r1"), "running-config", "show running-config", t0, []byte("hostname lab-r1\n"), 0)
	second, err := s.Put(dev("lab-r1"), "running-config", "show running-config", t0.Add(time.Hour), []byte("hostname lab-r1\nntp server 172.16.0.1\n"), 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if second.Unchanged || second.Path == first.Path {
		t.Fatal("changed capture reused the previous file")
	}
	if got := captureFiles(t, filepath.Dir(first.Path)); len(got) != 2 {
		t.Errorf("got %d files, want 2: %v", len(got), got)
	}
}

// Reverting a config to an earlier state must still write a file. Comparing
// against the LAST stored capture rather than against any capture is what
// makes history a timeline instead of a set.
func TestRevertingToAnEarlierConfigStillWritesAFile(t *testing.T) {
	s := store(t)
	a := []byte("hostname lab-r1\n")
	b := []byte("hostname lab-r1\nntp server 172.16.0.1\n")
	s.Put(dev("lab-r1"), "running-config", "show running-config", t0, a, 0)
	s.Put(dev("lab-r1"), "running-config", "show running-config", t0.Add(time.Hour), b, 0)
	back, err := s.Put(dev("lab-r1"), "running-config", "show running-config", t0.Add(2*time.Hour), a, 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if back.Unchanged {
		t.Fatal("a revert was treated as unchanged; history would show the config never came back")
	}
}

func TestTwoCapturesInTheSameSecondDoNotOverwriteEachOther(t *testing.T) {
	s := store(t)
	first, _ := s.Put(dev("lab-r1"), "running-config", "show running-config", t0, []byte("one\n"), 0)
	second, err := s.Put(dev("lab-r1"), "running-config", "show running-config", t0, []byte("two\n"), 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if first.Path == second.Path {
		t.Fatal("same-second captures collided on one filename")
	}
	if got, _ := os.ReadFile(first.Path); string(got) != "one\n" {
		t.Errorf("the first capture was overwritten: %q", got)
	}
}

// Windows will not accept a colon in a filename, and this ships to the
// Microsoft Store.
func TestCaptureFilenamesAreLegalOnWindows(t *testing.T) {
	s := store(t)
	art, _ := s.Put(dev("lab-r1"), "running-config", "show running-config", t0, []byte("x\n"), 0)
	name := filepath.Base(art.Path)
	if strings.ContainsAny(name, `:<>"|?*\/`) {
		t.Errorf("filename %q contains a character Windows rejects", name)
	}
	if name != "2026-07-31T15-04-22Z.txt" {
		t.Errorf("filename = %q, want the UTC stamp", name)
	}
}

// Two different devices whose names sanitize to the same slug must not end up
// sharing one config history. This is the failure the whole naming scheme
// exists to prevent, so it has to be loud. A name carrying a path separator
// is the case worth pinning: "lab/r1" both folds onto "lab-r1" and would be
// a traversal if it were ever used raw.
func TestSlugCollisionBetweenDifferentDevicesIsRefused(t *testing.T) {
	s := store(t)
	if _, err := s.Put(dev("lab-r1"), "running-config", "show running-config", t0, []byte("a\n"), 0); err != nil {
		t.Fatalf("put: %v", err)
	}
	_, err := s.Put(dev("lab/r1"), "running-config", "show running-config", t0, []byte("b\n"), 0)
	if err == nil {
		t.Fatal("a second device silently joined the first device's history")
	}
	if !strings.Contains(err.Error(), "lab-r1") || !strings.Contains(err.Error(), "lab/r1") {
		t.Errorf("error does not name both sides of the conflict: %v", err)
	}
}

// The other half of the same rule: a name that differs only in case is the
// SAME device, not a collision. Device prompts are not case-stable — a box
// reached by DNS and by its own prompt can report either — and refusing that
// would turn an ordinary capture into an error.
func TestCaseDifferenceIsTheSameDeviceNotACollision(t *testing.T) {
	s := store(t)
	if _, err := s.Put(dev("lab-r1"), "running-config", "show running-config", t0, []byte("a\n"), 0); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := s.Put(dev("LAB-R1"), "running-config", "show running-config", t0.Add(time.Hour), []byte("b\n"), 0); err != nil {
		t.Fatalf("a case difference was refused as a collision: %v", err)
	}
	h, _ := s.History("lab-r1", "running-config")
	if len(h) != 2 {
		t.Errorf("history = %d entries, want 2 in one shared history", len(h))
	}
}

func TestDeviceInfoKeepsFirstSeenAndAccumulatesAliases(t *testing.T) {
	s := store(t)
	d := DeviceInfo{Canonical: "lab-r1", Aliases: []string{"172.16.1.2"}, Platform: "cisco_ios"}
	s.Put(d, "running-config", "show running-config", t0, []byte("a\n"), 0)

	d2 := DeviceInfo{Canonical: "lab-r1", Aliases: []string{"lab-r1.lab.example"}}
	s.Put(d2, "running-config", "show running-config", t0.Add(time.Hour), []byte("b\n"), 0)

	info, err := readDeviceInfo(filepath.Join(s.Root(), "devices", "lab-r1"))
	if err != nil || info == nil {
		t.Fatalf("read device.json: %v", err)
	}
	if !info.FirstSeen.Equal(t0) {
		t.Errorf("FirstSeen moved to %s; it should stay at the first capture", info.FirstSeen)
	}
	if !info.LastSeen.Equal(t0.Add(time.Hour)) {
		t.Errorf("LastSeen = %s, want the latest capture", info.LastSeen)
	}
	if len(info.Aliases) != 2 {
		t.Errorf("aliases = %v, want both accumulated", info.Aliases)
	}
	if info.Platform != "cisco_ios" {
		t.Errorf("platform lost on the second capture: %q", info.Platform)
	}
}

// Capture types are separate directories, so the same device's config and
// inventory never interleave in one history.
func TestCaptureTypesAreSeparateHistories(t *testing.T) {
	s := store(t)
	s.Put(dev("lab-r1"), "running-config", "show running-config", t0, []byte("cfg\n"), 0)
	s.Put(dev("lab-r1"), "inventory", "show inventory", t0, []byte("inv\n"), 0)

	cfg, _ := s.History("lab-r1", "running-config")
	inv, _ := s.History("lab-r1", "inventory")
	if len(cfg) != 1 || len(inv) != 1 {
		t.Fatalf("histories bled together: config %d, inventory %d", len(cfg), len(inv))
	}
	if cfg[0].SHA256 == inv[0].SHA256 {
		t.Error("the two types stored the same content")
	}
}

func TestHistoryOfAnUncapturedDeviceIsEmptyNotAnError(t *testing.T) {
	s := store(t)
	h, err := s.History("lab-r99", "running-config")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(h) != 0 {
		t.Errorf("got %d entries for a device never captured", len(h))
	}
}

func TestSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"lab-r1", "lab-r1"},
		{"lab-r1.lab.example", "lab-r1.lab.example"},
		{"LAB-R1", "lab-r1"},
		{"172.16.1.2", "172.16.1.2"},
		{"eng spine 1", "eng-spine-1"},
		{"lab/r1", "lab-r1"},
		{`lab\r1`, "lab-r1"},
		{"con", "con-dev"},
		{"..lab-r1..", "lab-r1"},
	}
	for _, c := range cases {
		got, err := Slug(c.in)
		if err != nil {
			t.Errorf("Slug(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := Slug("///"); err == nil {
		t.Error("a name with no usable characters should be an error, not an empty directory")
	}
}

func TestDevicesListsCanonicalNamesNotSlugs(t *testing.T) {
	s := store(t)
	s.Put(dev("LAB-R1.lab.example"), "running-config", "show running-config", t0, []byte("a\n"), 0)
	got, err := s.Devices()
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(got) != 1 || got[0].Canonical != "LAB-R1.lab.example" {
		t.Errorf("Devices() = %v, want the canonical name as captured", got)
	}
}

func captureFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".txt") {
			out = append(out, e.Name())
		}
	}
	return out
}
