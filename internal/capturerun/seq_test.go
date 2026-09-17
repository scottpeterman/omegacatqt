package capturerun

import (
	"errors"
	"testing"
)

// A view that only ever pulls RowsSince, merging by pair, must end with
// exactly the table Rows reports -- however the pulls interleave with the
// events. Pulling after every event is the finest interleaving; if nothing is
// lost there, nothing is lost at any coarser one.
func TestRowsSinceRebuildsTheTable(t *testing.T) {
	run := New()
	view := map[Pair]Row{}
	var seq uint64
	pull := func() {
		rows, next := run.RowsSince(seq)
		for _, r := range rows {
			view[Pair{r.Identity, r.Type}] = r
		}
		seq = next
	}

	emit := run.Emit()
	for _, ev := range DemoEvents() {
		emit.Send(ev)
		pull()
	}
	run.Finish()
	pull()

	want := run.Rows()
	if len(view) != len(want) {
		t.Fatalf("view holds %d rows, run has %d", len(view), len(want))
	}
	for _, w := range want {
		got, ok := view[Pair{w.Identity, w.Type}]
		if !ok {
			t.Errorf("%s/%s never reached the view", w.Identity, w.Type)
			continue
		}
		if got.State != w.State || got.Platform != w.Platform || got.Name != w.Name ||
			got.Bytes != w.Bytes || got.Detail != w.Detail || got.Seq != w.Seq {
			t.Errorf("%s/%s: view has %+v, run has %+v", w.Identity, w.Type, got, w)
		}
	}
	if more, _ := run.RowsSince(seq); len(more) != 0 {
		t.Errorf("%d rows still reported after the last pull", len(more))
	}
}

// A late platform stamp changes rows that were opened before it; those rows
// must come back from RowsSince, or a view shows a blank platform column for
// a device the run knows the platform of.
func TestPlatformStampIsAChange(t *testing.T) {
	run := New()
	emit := run.Emit()
	emit.Send(Event{Kind: KindCaptureStart, Identity: "lab-r1", Type: "running-config"})
	_, seq := run.RowsSince(0)

	emit.Send(Event{Kind: KindPlatform, Identity: "lab-r1", Platform: "cisco_ios"})
	rows, _ := run.RowsSince(seq)
	if len(rows) != 1 || rows[0].Platform != "cisco_ios" {
		t.Fatalf("after the platform event RowsSince = %+v, want the row with its platform", rows)
	}
}

// Finish settles rows with no event behind them. Without a seq of their own
// a pulling view would show them running forever.
func TestFinishSettledRowsAreSeen(t *testing.T) {
	run := New()
	emit := run.Emit()
	emit.Send(Event{Kind: KindCaptureStart, Identity: "lab-r1", Type: "running-config"})
	emit.Send(Event{Kind: KindCaptureStart, Identity: "lab-r1", Type: "inventory"})
	emit.Send(Event{Kind: KindStored, Identity: "lab-r1", Type: "inventory", Bytes: 10})
	_, seq := run.RowsSince(0)

	run.Finish()
	rows, next := run.RowsSince(seq)
	if len(rows) != 1 || rows[0].Type != "running-config" || rows[0].State != StateFailed {
		t.Fatalf("after Finish RowsSince = %+v, want the one row Finish settled", rows)
	}
	if next <= seq {
		t.Errorf("Finish changed a row without moving seq (%d -> %d)", seq, next)
	}
}

func TestDecisionsSinceIsIncrementalAndComplete(t *testing.T) {
	run := New()
	emit := run.Emit()
	var got []Event
	var seq uint64
	for _, ev := range DemoEvents() {
		emit.Send(ev)
		evs, next := run.DecisionsSince(seq)
		got = append(got, evs...)
		seq = next
	}
	want := run.Decisions()
	if len(got) != len(want) {
		t.Fatalf("pulled %d decisions, run has %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Seq == 0 {
			t.Errorf("decision %d: pulled seq %d, run has %d", i, got[i].Seq, want[i].Seq)
		}
	}
}

func TestProgressCountsSettledPairs(t *testing.T) {
	run := New()
	emit := run.Emit()
	emit.Send(Event{Kind: KindCaptureStart, Identity: "lab-r1", Type: "running-config"})
	emit.Send(Event{Kind: KindCaptureStart, Identity: "lab-r2", Type: "running-config"})
	emit.Send(Event{Kind: KindCaptureFail, Identity: "lab-r2", Type: "running-config", Err: errors.New("timeout")})

	p := run.Progress()
	if p.Finished || p.Total != 2 || p.Settled != 1 || len(p.Running) != 1 || p.Running[0].Identity != "lab-r1" {
		t.Fatalf("mid-run progress = %+v", p)
	}
	run.Finish()
	p = run.Progress()
	if !p.Finished || p.Settled != 2 || len(p.Running) != 0 {
		t.Fatalf("finished progress = %+v", p)
	}
}
