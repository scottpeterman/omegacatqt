package tfsmfire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The Go engine must choose what the Python engine chose, with the same score
// to the last bit, for every recorded case -- wrong picks included. The cases
// come from scripts/tfsm_parity.py run against the shipped seed database; see
// that script for how to regenerate them after the seed changes.
func TestParityWithPythonEngine(t *testing.T) {
	if raceEnabled {
		// Thousands of parses over the whole corpus take minutes under the
		// race detector and exercise no concurrency;
		// TestFindBestIsSafeConcurrently is the race coverage.
		t.Skip("corpus parity runs without -race")
	}
	var golden struct {
		DBRows int `json:"db_rows"`
		Cases  []struct {
			Sample  string `json:"sample"`
			Hint    string `json:"hint"`
			Winner  string `json:"winner"`
			Score   string `json:"score"`
			Records int    `json:"records"`
		} `json:"cases"`
	}
	b, err := os.ReadFile(filepath.Join("testdata", "parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}

	e := seedEngine(t)
	if n := len(e.Templates()); n != golden.DBRows {
		t.Fatalf("seed has %d templates, golden was recorded against %d; regenerate testdata/parity.json", n, golden.DBRows)
	}

	agree, known := 0, 0
	for _, c := range golden.Cases {
		tpl, ok := e.Template(c.Sample)
		if !ok {
			t.Errorf("sample template %q is not in the seed", c.Sample)
			continue
		}
		want, err := strconv.ParseFloat(c.Score, 64)
		if err != nil {
			t.Fatalf("golden score %q: %v", c.Score, err)
		}
		got := e.FindBest(tpl.Sample, c.Hint)
		if _, incompatible := knownIncompatible[c.Winner]; incompatible && got.Template != c.Winner {
			// Python's winner cannot run in Go at all, so Go cannot choose
			// it. Expected, and listed in samples_test.go with the reason.
			known++
			continue
		}
		if got.Template != c.Winner || got.Score != want || len(got.Records) != c.Records {
			t.Errorf("hint %-30s sample %-45s python: %s %v (%d records)  go: %s %v (%d records)",
				c.Hint, c.Sample, c.Winner, want, c.Records, got.Template, got.Score, len(got.Records))
			continue
		}
		agree++
	}
	t.Logf("%d / %d cases identical to the Python engine; %d where Python's winner cannot compile in Go",
		agree, len(golden.Cases), known)
}

// seedEngine opens the shipped database from a temporary copy made by
// EnsureDB, which exercises the first-run path at the same time.
func seedEngine(t *testing.T) *Engine {
	t.Helper()
	path, created, err := EnsureDB(t.TempDir())
	if err != nil || !created {
		t.Fatalf("EnsureDB: %v (created %v)", err, created)
	}
	e, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
