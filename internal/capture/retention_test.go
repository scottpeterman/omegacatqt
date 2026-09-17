// internal/capture/retention_test.go
//
// Bounded capture types: keeping the newest N versions and dropping the rest.
//
// The invariant every test here is really defending is that history.jsonl and
// the directory agree. Pruning is the only operation in the store that removes
// something, and the store view reads a version list out of history and then
// opens the file each line names — so a prune that unlinks a file without
// removing its history line produces a row that errors when clicked, which is
// the one failure mode a reader cannot recover from on its own.
package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func retDev(name string) DeviceInfo {
	return DeviceInfo{Canonical: name, Platform: "arista_eos"}
}

// typeDirOf reaches into the layout deliberately: these tests are about what
// is on disk, so going through the Browser would test the reader's opinion of
// the directory rather than the directory.
func typeDirOf(t *testing.T, s *FileStore, canonical, typ string) string {
	t.Helper()
	slug, err := Slug(canonical)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	return filepath.Join(s.Root(), "devices", slug, typ)
}

func TestKeepBoundsTheVersionsOnDisk(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	for i := 0; i < 7; i++ {
		body := []byte("arp entry generation " + string(rune('a'+i)) + "\n")
		if _, err := s.Put(retDev("lab-leaf-1"), "arp-table", "show ip arp",
			base.Add(time.Duration(i)*time.Hour), body, 5); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	files := captureFiles(t, typeDirOf(t, s, "lab-leaf-1", "arp-table"))
	if len(files) != 5 {
		t.Fatalf("kept %d files, want 5: %v", len(files), files)
	}

	hist, err := s.History("lab-leaf-1", "arp-table")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 5 {
		t.Fatalf("history has %d entries, want 5", len(hist))
	}

	// The newest capture has to be the one that survived, not merely five
	// of the seven.
	newest := hist[len(hist)-1]
	got, err := s.Read("lab-leaf-1", "arp-table", newest.File)
	if err != nil {
		t.Fatalf("read newest: %v", err)
	}
	if want := "arp entry generation g\n"; string(got) != want {
		t.Errorf("newest file holds %q, want %q", got, want)
	}
}

// The guard on every existing capture type. Retention is opt-in and a config
// history must be untouched by its existence.
func TestKeepZeroRetainsEverything(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	for i := 0; i < 7; i++ {
		body := []byte("hostname lab-r1\n! rev " + string(rune('a'+i)) + "\n")
		if _, err := s.Put(retDev("lab-r1"), "running-config", "show running-config",
			base.Add(time.Duration(i)*time.Hour), body, 0); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	if files := captureFiles(t, typeDirOf(t, s, "lab-r1", "running-config")); len(files) != 7 {
		t.Fatalf("keep=0 left %d files, want all 7: %v", len(files), files)
	}
}

// A run of unchanged attempts must not reduce what is on disk, and must still
// be recorded in full.
//
// Pruning is skipped on the unchanged path as a matter of cost rather than
// correctness — running it there would be a no-op, since nothing was written
// and the survivors would be the same set. What this pins down is the visible
// half: stability does not cost a device its version history, and the
// attempts are all still in the record.
func TestAnUnchangedAttemptDoesNotPrune(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	first := []byte("generation one\n")
	second := []byte("generation two\n")
	if _, err := s.Put(retDev("lab-leaf-1"), "arp-table", "show ip arp", base, first, 2); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if _, err := s.Put(retDev("lab-leaf-1"), "arp-table", "show ip arp", base.Add(time.Hour), second, 2); err != nil {
		t.Fatalf("put second: %v", err)
	}
	for i := 0; i < 4; i++ {
		art, err := s.Put(retDev("lab-leaf-1"), "arp-table", "show ip arp",
			base.Add(time.Duration(2+i)*time.Hour), second, 2)
		if err != nil {
			t.Fatalf("put unchanged %d: %v", i, err)
		}
		if !art.Unchanged {
			t.Fatalf("attempt %d should have been unchanged", i)
		}
	}

	if files := captureFiles(t, typeDirOf(t, s, "lab-leaf-1", "arp-table")); len(files) != 2 {
		t.Fatalf("have %d files, want both versions still present: %v", len(files), files)
	}
	hist, err := s.History("lab-leaf-1", "arp-table")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 6 {
		t.Errorf("history has %d entries, want all 6 attempts recorded", len(hist))
	}
}

// keep counts VERSIONS, not attempts, and the difference only becomes visible
// when unchanged attempts sit between two stored ones.
//
// An implementation that truncates history to its last keep lines looks
// correct against a device that changes every run — every entry is a version,
// so the two counts agree. Put a stable stretch in the middle and it collapses
// a real version history down to whatever fits, which is the exact shape of a
// device that was quiet over a weekend.
func TestUnchangedAttemptsDoNotCountAgainstTheVersionLimit(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)
	dv := retDev("lab-leaf-1")

	put := func(n int, body string, wantUnchanged bool) {
		t.Helper()
		art, err := s.Put(dv, "arp-table", "show ip arp",
			base.Add(time.Duration(n)*time.Minute), []byte(body), 3)
		if err != nil {
			t.Fatalf("put %d: %v", n, err)
		}
		if art.Unchanged != wantUnchanged {
			t.Fatalf("put %d: Unchanged = %v, want %v", n, art.Unchanged, wantUnchanged)
		}
	}

	put(0, "generation a\n", false)
	put(1, "generation b\n", false)
	put(2, "generation b\n", true)
	put(3, "generation b\n", true)
	put(4, "generation b\n", true)
	put(5, "generation c\n", false)

	files := captureFiles(t, typeDirOf(t, s, "lab-leaf-1", "arp-table"))
	if len(files) != 3 {
		t.Fatalf("kept %d files, want all 3 versions: %v", len(files), files)
	}

	hist, err := s.History("lab-leaf-1", "arp-table")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range hist {
		seen[e.File] = true
		if _, err := s.Read("lab-leaf-1", "arp-table", e.File); err != nil {
			t.Errorf("history names %q but it cannot be read: %v", e.File, err)
		}
	}
	if len(seen) != 3 {
		t.Errorf("history names %d distinct files, want 3", len(seen))
	}
}

// The mutation this exists to kill: pruning by filename instead of by history
// order.
//
// Three captures in the same second are stored as "<stamp>.txt", "<stamp>-1"
// and "<stamp>-2", newest last. Sorted as strings they come out -1, -2, then
// the bare stamp, because "-" is 0x2D and "." is 0x2E — so an implementation
// that keeps the lexically-last two keeps the OLDEST capture and deletes a
// newer one. Nothing about that failure is visible in a test whose captures
// are a minute apart.
func TestPruneOrdersByHistoryNotFilename(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	var stored []string
	for i := 0; i < 3; i++ {
		body := []byte("same second generation " + string(rune('a'+i)) + "\n")
		art, err := s.Put(retDev("lab-leaf-1"), "mac-table", "show mac address-table", base, body, 2)
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		stored = append(stored, filepath.Base(art.Path))
	}

	// Confirm the fixture actually produced the collision-suffixed names,
	// otherwise this test passes for the wrong reason.
	if stored[0] == stored[1] || !strings.Contains(stored[1], "-1.txt") {
		t.Fatalf("fixture did not produce same-second suffixes: %v", stored)
	}

	files := captureFiles(t, typeDirOf(t, s, "lab-leaf-1", "mac-table"))
	if len(files) != 2 {
		t.Fatalf("kept %d files, want 2: %v", len(files), files)
	}
	got := strings.Join(files, " ")
	if strings.Contains(got, stored[0]) {
		t.Errorf("kept the oldest capture %q; files are %v", stored[0], files)
	}
	if !strings.Contains(got, stored[2]) {
		t.Errorf("dropped the newest capture %q; files are %v", stored[2], files)
	}
}

// Every surviving history line must name a file that is actually there. This
// is what the store view walks, and a dangling line is a row that errors when
// opened.
func TestPrunedHistoryNeverNamesAMissingFile(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	for i := 0; i < 9; i++ {
		body := []byte("generation " + string(rune('a'+i)) + "\n")
		if _, err := s.Put(retDev("lab-leaf-1"), "arp-table", "show ip arp",
			base.Add(time.Duration(i)*time.Minute), body, 3); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	hist, err := s.History("lab-leaf-1", "arp-table")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) == 0 {
		t.Fatal("history is empty")
	}
	for _, e := range hist {
		if e.File == "" {
			continue
		}
		if _, err := s.Read("lab-leaf-1", "arp-table", e.File); err != nil {
			t.Errorf("history names %q but it cannot be read: %v", e.File, err)
		}
	}
}

// A prune interrupted between the history rewrite and the unlink leaves files
// nothing references. The next prune has to collect them, which is what makes
// a refused delete — Windows with the file open in the viewer — heal itself
// rather than leak forever.
func TestPruneSweepsFilesHistoryNoLongerNames(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	if _, err := s.Put(retDev("lab-leaf-1"), "arp-table", "show ip arp", base, []byte("one\n"), 2); err != nil {
		t.Fatalf("put: %v", err)
	}
	dir := typeDirOf(t, s, "lab-leaf-1", "arp-table")
	orphan := filepath.Join(dir, "2020-01-01T00-00-00Z.txt")
	if err := os.WriteFile(orphan, []byte("left behind\n"), 0o600); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}

	if _, err := s.Put(retDev("lab-leaf-1"), "arp-table", "show ip arp",
		base.Add(time.Hour), []byte("two\n"), 2); err != nil {
		t.Fatalf("put: %v", err)
	}

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan file survived the prune (stat err %v)", err)
	}
	if files := captureFiles(t, dir); len(files) != 2 {
		t.Errorf("have %v, want the two real versions", files)
	}
}

// Retention has to survive the reader too: after a prune, Types must still
// report the newest file and that file must open.
func TestTypesReportsTheNewestFileAfterPruning(t *testing.T) {
	s := store(t)
	base := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	for i := 0; i < 6; i++ {
		body := []byte("generation " + string(rune('a'+i)) + "\n")
		if _, err := s.Put(retDev("lab-leaf-1"), "mac-table", "show mac address-table",
			base.Add(time.Duration(i)*time.Minute), body, 2); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	types, err := s.Types("lab-leaf-1")
	if err != nil {
		t.Fatalf("types: %v", err)
	}
	if len(types) != 1 {
		t.Fatalf("got %d types, want 1", len(types))
	}
	ti := types[0]
	if ti.Stored != 2 {
		t.Errorf("Stored = %d, want 2 surviving versions", ti.Stored)
	}
	got, err := s.Read("lab-leaf-1", "mac-table", ti.File)
	if err != nil {
		t.Fatalf("read newest: %v", err)
	}
	if want := "generation f\n"; string(got) != want {
		t.Errorf("newest holds %q, want %q", got, want)
	}
}

// The bounded types are the reason any of this exists, so assert the spec
// values rather than leaving them to be quietly edited to zero later.
func TestTheBoundedTypesDeclareARetention(t *testing.T) {
	for _, s := range []Spec{ARPTable, MACTable} {
		if s.Keep <= 0 {
			t.Errorf("%s declares Keep=%d; a type that writes a file every run needs a bound",
				s.Type, s.Keep)
		}
	}
	for _, s := range []Spec{RunningConfig, StartupConfig, Inventory} {
		if s.Keep != 0 {
			t.Errorf("%s declares Keep=%d; configuration history is the record and must not be pruned",
				s.Type, s.Keep)
		}
	}
}
