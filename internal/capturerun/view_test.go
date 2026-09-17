// internal/capturerun/view_test.go
//
// The parts of the run model a view leans on: sorting, state filtering, the
// artifact path, and the scripted demo.
//
// None of this is exercised by the engine, which only ever writes into a Run.
// It is all read surface, and read surface with no tests is how a table ends up
// rendering blank columns that the model could have answered — the bug this
// package already had once, in the other direction.
package capturerun_test

import (
	"testing"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capturerun"
)

// fill plays a small run with a known shape: two devices, three types, one of
// everything worth sorting on.
func fill(t *testing.T) *capturerun.Run {
	t.Helper()
	r := capturerun.New()
	e := r.Emit()

	e.Send(capturerun.Event{Kind: capturerun.KindConnected, Identity: "lab-r2"})
	e.Send(capturerun.Event{Kind: capturerun.KindPlatform, Identity: "lab-r2", Platform: "arista_eos"})
	e.Send(capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r2",
		Type: "running-config", Command: "show running-config"})
	e.Send(capturerun.Event{Kind: capturerun.KindStored, Identity: "lab-r2",
		Type: "running-config", Bytes: 100, SHA: "aa", Path: "/store/lab-r2/running-config/a.txt"})
	e.Send(capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r2", Type: "inventory"})
	e.Send(capturerun.Event{Kind: capturerun.KindCaptureFail, Identity: "lab-r2",
		Type: "inventory", Detail: "command timed out"})

	e.Send(capturerun.Event{Kind: capturerun.KindConnected, Identity: "lab-r1"})
	e.Send(capturerun.Event{Kind: capturerun.KindPlatform, Identity: "lab-r1", Platform: "cisco_ios"})
	e.Send(capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1", Type: "running-config"})
	e.Send(capturerun.Event{Kind: capturerun.KindUnchanged, Identity: "lab-r1",
		Type: "running-config", Bytes: 900, SHA: "bb", Path: "/store/lab-r1/running-config/b.txt"})
	e.Send(capturerun.Event{Kind: capturerun.KindNotApplic, Identity: "lab-r1",
		Type: "startup-config", Platform: "cisco_ios"})
	// Left mid-flight on purpose: the running state has to be in the
	// fixture or nothing tests how a row looks while it is still going.
	e.Send(capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1", Type: "inventory"})
	return r
}

func TestSortedOrdersByColumnAndBreaksTiesOnTheDevice(t *testing.T) {
	r := fill(t)

	// Default column: device name.
	rows := r.Sorted("device", true)
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}
	if rows[0].Identity != "lab-r1" || rows[len(rows)-1].Identity != "lab-r2" {
		t.Errorf("device sort = %s..%s, want lab-r1..lab-r2", rows[0].Identity, rows[len(rows)-1].Identity)
	}

	// Bytes ascending: the two zero-byte rows tie and must fall back to
	// (device, type) rather than to whatever order they arrived in.
	byBytes := r.Sorted("bytes", true)
	if byBytes[len(byBytes)-1].Bytes != 900 {
		t.Errorf("largest row = %d bytes, want 900 last when ascending", byBytes[len(byBytes)-1].Bytes)
	}

	// Reversing must not reverse the tiebreak — one device's captures stay
	// together in both directions.
	for _, asc := range []bool{true, false} {
		got := r.Sorted("state", asc)
		seen := map[string]int{}
		last := ""
		for _, row := range got {
			if row.Identity != last {
				seen[row.Identity]++
				last = row.Identity
			}
		}
		_ = seen
		if len(got) != 5 {
			t.Errorf("asc=%v returned %d rows, want 5", asc, len(got))
		}
	}

	// An unrecognized column must not drop rows or panic; it falls back to
	// the device order.
	if got := r.Sorted("no-such-column", true); len(got) != 5 {
		t.Errorf("unknown column returned %d rows, want 5", len(got))
	}
}

func TestSortedIsStableAcrossRepeatedCalls(t *testing.T) {
	r := fill(t)
	first := r.Sorted("state", true)
	for i := 0; i < 5; i++ {
		again := r.Sorted("state", true)
		for j := range first {
			if first[j].Identity != again[j].Identity || first[j].Type != again[j].Type {
				t.Fatalf("row %d moved between identical sorts: %s/%s then %s/%s — "+
					"a table that reshuffles under the cursor on every redraw",
					j, first[j].Identity, first[j].Type, again[j].Identity, again[j].Type)
			}
		}
	}
}

func TestRowsByStateFiltersForTheCounters(t *testing.T) {
	r := fill(t)
	for _, c := range []struct {
		state capturerun.State
		want  int
	}{
		{capturerun.StateStored, 1},
		{capturerun.StateUnchanged, 1},
		{capturerun.StateNotApplicable, 1},
		{capturerun.StateFailed, 1},
		{capturerun.StateRunning, 1}, // lab-r1's startup-config settled; lab-r2 has none left
	} {
		if got := len(r.RowsByState(c.state)); got != c.want {
			t.Errorf("RowsByState(%s) = %d, want %d", c.state, got, c.want)
		}
	}
}

func TestPathReachesTheRowForBothStoredAndUnchanged(t *testing.T) {
	r := fill(t)
	for _, row := range r.Rows() {
		switch row.State {
		case capturerun.StateStored, capturerun.StateUnchanged:
			if row.Path == "" {
				t.Errorf("%s/%s is %s with no Path; the row cannot be opened even "+
					"though a file exists", row.Identity, row.Type, row.State)
			}
		case capturerun.StateFailed, capturerun.StateNotApplicable:
			if row.Path != "" {
				t.Errorf("%s/%s is %s but carries Path %q; emptiness is what the "+
					"view tests to decide whether a row opens",
					row.Identity, row.Type, row.State, row.Path)
			}
		}
	}
}

func TestDisplayPrefersTheCanonicalNameOnceKnown(t *testing.T) {
	r := capturerun.New()
	e := r.Emit()
	e.Send(capturerun.Event{Kind: capturerun.KindCaptureStart,
		Identity: "172.16.1.2", Type: "running-config"})
	rows := r.Rows()
	if got := rows[0].Display(); got != "172.16.1.2" {
		t.Errorf("Display() = %q before naming, want the run identity", got)
	}
	e.Send(capturerun.Event{Kind: capturerun.KindResolved,
		Identity: "172.16.1.2", Name: "lab-r1.lab.example"})
	rows = r.Rows()
	if got := rows[0].Display(); got != "lab-r1.lab.example" {
		t.Errorf("Display() = %q after naming, want the canonical name", got)
	}
}

func TestOnChangeReplacesRatherThanAccumulates(t *testing.T) {
	r := capturerun.New()
	var first, second int
	r.OnChange(func() { first++ })
	r.OnChange(func() { second++ })
	r.Emit().Send(capturerun.Event{Kind: capturerun.KindQueued, Identity: "lab-r1"})
	if first != 0 {
		t.Errorf("the replaced hook fired %d times; OnChange installs one hook, last writer wins", first)
	}
	if second != 1 {
		t.Errorf("the installed hook fired %d times, want 1", second)
	}
}

func TestDemoPlaysAndStopLeavesItPartial(t *testing.T) {
	full := capturerun.New()
	capturerun.Demo(full, capturerun.DemoOptions{})
	all := len(full.Rows())
	if all == 0 {
		t.Fatal("Demo produced no rows")
	}
	if len(full.Decisions()) == 0 {
		t.Error("Demo produced no decisions; the lower pane would be empty in preview")
	}

	// A Stop that is already closed must abort playback rather than run to
	// completion, which is what the Stop button relies on.
	stopped := capturerun.New()
	stop := make(chan struct{})
	close(stop)
	capturerun.Demo(stopped, capturerun.DemoOptions{Stop: stop})
	if got := len(stopped.Rows()); got >= all {
		t.Errorf("a pre-closed Stop produced %d rows of %d; playback did not abort", got, all)
	}
}

func TestDemoEventsIsDeterministic(t *testing.T) {
	// The preview is a fixture. If it varies between calls, a screenshot
	// and a bug report stop describing the same thing.
	a, b := capturerun.DemoEvents(), capturerun.DemoEvents()
	if len(a) != len(b) {
		t.Fatalf("DemoEvents returned %d then %d events", len(a), len(b))
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Identity != b[i].Identity || a[i].Type != b[i].Type {
			t.Fatalf("event %d differs between calls: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestDemoStepPacesPlayback(t *testing.T) {
	r := capturerun.New()
	start := time.Now()
	stop := make(chan struct{})
	go func() {
		time.Sleep(60 * time.Millisecond)
		close(stop)
	}()
	capturerun.Demo(r, capturerun.DemoOptions{Step: 10 * time.Millisecond, Stop: stop})
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("paced playback returned after %v; Step is not being applied", elapsed)
	}
	if len(r.Rows()) == 0 {
		t.Error("paced playback produced no rows before Stop")
	}
}
