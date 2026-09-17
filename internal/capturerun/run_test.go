// internal/capturerun/run_test.go
//
// The run model on its own, driven by hand-written event streams.
//
// The engine exercises a fraction of this file. It emits a well-formed stream
// in a fixed order and never sends the same terminal event twice, never dies
// with rows open, and never reorders a platform event behind the rows it has
// to stamp. A UI hits all of those: it is the consumer that reads counts while
// the run is in flight, that survives an engine bug rather than crashing on
// it, and that shows a number a person will believe. A wrong count in a backup
// tool is worse than a missing one.
//
// So these tests drive the model directly rather than through a device.
package capturerun_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capturerun"
)

// send delivers through Emit.Send rather than calling the sink directly, which
// is how the engine does it and is what fills the timestamps.
func send(r *capturerun.Run, evs ...capturerun.Event) {
	e := r.Emit()
	for _, ev := range evs {
		e.Send(ev)
	}
}

func rowFor(t *testing.T, r *capturerun.Run, identity, typ string) capturerun.Row {
	t.Helper()
	for _, row := range r.Rows() {
		if row.Identity == identity && row.Type == typ {
			return row
		}
	}
	t.Fatalf("no row for %s/%s", identity, typ)
	return capturerun.Row{}
}

// A row left running when the run ends is an engine bug — a missing emit site,
// not a device that took too long. The reason it settles with must say so,
// because the plausible-sounding alternative sends someone to look at the
// device. crawlrun spent a session on exactly that: "run ended before this
// device completed" was read as a timeout, and it was a KindReached that had
// never been wired.
func TestFinishSettlesMidFlightRowsAsAnEngineBug(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindQueued, Identity: "lab-r1"},
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1",
			Type: "running-config", Command: "show running-config"},
	)
	if c := r.Counts(); c.Running != 1 {
		t.Fatalf("Counts.Running = %d before Finish, want 1", c.Running)
	}

	r.Finish()

	row := rowFor(t, r, "lab-r1", "running-config")
	if row.State != capturerun.StateFailed {
		t.Errorf("State = %v after Finish, want failed", row.State)
	}
	if row.Detail == "" {
		t.Error("swept row has no reason at all")
	}
	if row.Finished.IsZero() {
		t.Error("swept row was never given a finish time, so it reads as still running")
	}
	c := r.Counts()
	if c.Running != 0 || c.Failed != 1 {
		t.Errorf("Counts = %+v, want 0 running and 1 failed", c)
	}
}

// Finish must be safe to call twice. A UI wires it to both the engine
// returning and the window closing, and those can both happen.
func TestFinishIsIdempotent(t *testing.T) {
	r := capturerun.New()
	send(r, capturerun.Event{Kind: capturerun.KindCaptureStart,
		Identity: "lab-r1", Type: "running-config"})
	r.Finish()
	first := r.Counts()
	r.Finish()
	if second := r.Counts(); second != first {
		t.Errorf("counts changed on a second Finish: %+v then %+v", first, second)
	}
}

// A terminal event arriving twice must not be counted twice. This is not a
// hypothetical: the engine already emits both a device-level failure and a
// per-spec failure for an unreachable device, and any future retry or resume
// path is another chance to send one twice. Counting on the transition rather
// than recomputing from the map is what makes the counter survive it.
func TestARepeatedTerminalEventIsCountedOnce(t *testing.T) {
	r := capturerun.New()
	stored := capturerun.Event{Kind: capturerun.KindStored, Identity: "lab-r1",
		Type: "running-config", Bytes: 512, SHA: "abc"}
	send(r, stored, stored, stored)

	c := r.Counts()
	if c.Stored != 1 {
		t.Errorf("Counts.Stored = %d for three identical events, want 1", c.Stored)
	}
	if c.BytesStored != 512 {
		t.Errorf("Counts.BytesStored = %d, want 512 counted once", c.BytesStored)
	}
	if c.Running != 0 {
		t.Errorf("Counts.Running = %d, want 0", c.Running)
	}
}

// A pair that has already settled keeps its first outcome. A late failure
// arriving after a successful store must not rewrite the row: the artifact is
// on disk, and a row that says otherwise is a lie about a file that exists.
func TestTheFirstTerminalEventWins(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindStored, Identity: "lab-r1",
			Type: "running-config", Bytes: 100},
		capturerun.Event{Kind: capturerun.KindCaptureFail, Identity: "lab-r1",
			Type: "running-config", Err: errors.New("too late")},
	)
	row := rowFor(t, r, "lab-r1", "running-config")
	if row.State != capturerun.StateStored {
		t.Errorf("State = %v, want the first outcome to stand", row.State)
	}
	if c := r.Counts(); c.Stored != 1 || c.Failed != 0 {
		t.Errorf("Counts = %+v, want 1 stored and 0 failed", c)
	}
}

// A device that dies with captures already open settles every one of its rows.
// The engine cannot currently reach this — it fails before any capture starts
// — but a UI showing a row that says "running" forever is the failure mode
// that made this code exist, and the engine will grow a mid-session failure
// path the first time a session drops halfway through a tech-support.
func TestADeviceFailureSettlesItsOpenRows(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1",
			Type: "running-config"},
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1",
			Type: "inventory"},
		// A second device's rows must be left alone.
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r2",
			Type: "running-config"},
		capturerun.Event{Kind: capturerun.KindDeviceFail, Identity: "lab-r1",
			Err: errors.New("session dropped")},
	)
	for _, typ := range []string{"running-config", "inventory"} {
		row := rowFor(t, r, "lab-r1", typ)
		if row.State != capturerun.StateFailed {
			t.Errorf("lab-r1/%s State = %v, want failed", typ, row.State)
		}
		if row.Detail != "session dropped" {
			t.Errorf("lab-r1/%s Detail = %q, want the device's reason", typ, row.Detail)
		}
	}
	if row := rowFor(t, r, "lab-r2", "running-config"); row.State != capturerun.StateRunning {
		t.Errorf("lab-r2 was settled by another device's failure: %v", row.State)
	}
	c := r.Counts()
	if c.DevicesFailed != 1 || c.Failed != 2 || c.Running != 1 {
		t.Errorf("Counts = %+v, want 1 device failed, 2 pairs failed, 1 still running", c)
	}
}

// The same device failing twice is one failed device.
func TestARepeatedDeviceFailureCountsOnce(t *testing.T) {
	r := capturerun.New()
	fail := capturerun.Event{Kind: capturerun.KindDeviceFail, Identity: "lab-r1",
		Err: errors.New("no route to host")}
	send(r, fail, fail)
	if c := r.Counts(); c.DevicesFailed != 1 {
		t.Errorf("Counts.DevicesFailed = %d, want 1", c.DevicesFailed)
	}
}

// Platform and name are device facts held once and stamped onto rows. They
// have to land whichever side of the rows they arrive on: the engine
// fingerprints before any capture starts, but a resume, a replay from a saved
// event log, or a reordering under concurrency puts them the other way round.
func TestDeviceFactsStampRowsInEitherOrder(t *testing.T) {
	t.Run("facts before rows", func(t *testing.T) {
		r := capturerun.New()
		send(r,
			capturerun.Event{Kind: capturerun.KindPlatform, Identity: "lab-r1",
				Platform: "arista_eos"},
			capturerun.Event{Kind: capturerun.KindResolved, Identity: "lab-r1",
				Name: "lab-r1.lab.example"},
			capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1",
				Type: "running-config"},
		)
		row := rowFor(t, r, "lab-r1", "running-config")
		if row.Platform != "arista_eos" || row.Name != "lab-r1.lab.example" {
			t.Errorf("row = %q/%q, want the device facts copied on at creation",
				row.Platform, row.Name)
		}
	})

	t.Run("rows before facts", func(t *testing.T) {
		r := capturerun.New()
		send(r,
			capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1",
				Type: "running-config"},
			capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1",
				Type: "inventory"},
			capturerun.Event{Kind: capturerun.KindPlatform, Identity: "lab-r1",
				Platform: "arista_eos"},
			capturerun.Event{Kind: capturerun.KindNamed, Identity: "lab-r1",
				Name: "lab-r1.lab.example"},
		)
		for _, typ := range []string{"running-config", "inventory"} {
			row := rowFor(t, r, "lab-r1", typ)
			if row.Platform != "arista_eos" || row.Name != "lab-r1.lab.example" {
				t.Errorf("%s row = %q/%q, want the late facts stamped back on",
					typ, row.Platform, row.Name)
			}
		}
	})
}

// An empty Name must not erase a name already known. A device resolved from
// the binding store and then re-announced by a later event with no name would
// otherwise go anonymous halfway through the run.
func TestAnEmptyNameDoesNotEraseAKnownOne(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindResolved, Identity: "lab-r1",
			Name: "lab-r1.lab.example"},
		capturerun.Event{Kind: capturerun.KindNamed, Identity: "lab-r1"},
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1",
			Type: "running-config"},
	)
	if row := rowFor(t, r, "lab-r1", "running-config"); row.Name != "lab-r1.lab.example" {
		t.Errorf("Name = %q, want the known name kept", row.Name)
	}
}

// Unchanged is a success and contributes no bytes. A schedule where every
// device is unchanged is a healthy schedule, and a "bytes stored" figure that
// counts content nobody wrote makes the run look like it did work it did not.
func TestUnchangedCountsAsSuccessAndStoresNoBytes(t *testing.T) {
	r := capturerun.New()
	send(r, capturerun.Event{Kind: capturerun.KindUnchanged, Identity: "lab-r1",
		Type: "running-config", Bytes: 4096, SHA: "same"})
	c := r.Counts()
	if c.Unchanged != 1 || c.Stored != 0 {
		t.Errorf("Counts = %+v, want 1 unchanged and 0 stored", c)
	}
	if c.BytesStored != 0 {
		t.Errorf("Counts.BytesStored = %d, want 0 — nothing was written", c.BytesStored)
	}
	if !capturerun.StateUnchanged.OK() {
		t.Error("unchanged does not report OK; it is the expected outcome on a schedule")
	}
}

// Not-applicable is the third outcome and must carry a reason a person can
// read, since it is the one most easily mistaken for a failure.
func TestNotApplicableExplainsItself(t *testing.T) {
	r := capturerun.New()
	send(r, capturerun.Event{Kind: capturerun.KindNotApplic, Identity: "lab-mx1",
		Type: "startup-config", Platform: "juniper_junos"})
	row := rowFor(t, r, "lab-mx1", "startup-config")
	if row.State != capturerun.StateNotApplicable {
		t.Fatalf("State = %v, want not applicable", row.State)
	}
	if row.Detail == "" {
		t.Error("not-applicable row has no explanation, so it reads as a bare failure")
	}
	if !capturerun.StateNotApplicable.OK() {
		t.Error("not applicable does not report OK; it means nothing is wrong")
	}
}

// A failure with no error and no detail still gets a reason, because a blank
// Detail column on a failed row is the one place a table must not be silent.
func TestAFailureAlwaysHasAReason(t *testing.T) {
	r := capturerun.New()
	send(r, capturerun.Event{Kind: capturerun.KindCaptureFail,
		Identity: "lab-r1", Type: "running-config"})
	if row := rowFor(t, r, "lab-r1", "running-config"); row.Detail == "" {
		t.Error("a failed row was left with no reason at all")
	}
}

// RowsSorted is the reading order: everything about one box together, types in
// a stable order within it.
func TestRowsSortedGroupsByDeviceThenType(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r2", Type: "running-config"},
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1", Type: "running-config"},
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r2", Type: "inventory"},
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1", Type: "inventory"},
	)
	var got []string
	for _, row := range r.RowsSorted() {
		got = append(got, row.Identity+"/"+row.Type)
	}
	want := []string{
		"lab-r1/inventory", "lab-r1/running-config",
		"lab-r2/inventory", "lab-r2/running-config",
	}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("RowsSorted = %v, want %v", got, want)
		}
	}
	// Rows keeps arrival order, which is what a live view scrolls.
	if first := r.Rows()[0]; first.Identity != "lab-r2" {
		t.Errorf("Rows() reordered; it should stay in event order, got %q first", first.Identity)
	}
}

// Rows and Counts hand back snapshots. A UI that renders from a live map while
// the engine writes to it is a data race with a redraw in the middle.
func TestRowsAreSnapshotsNotLiveHandles(t *testing.T) {
	r := capturerun.New()
	send(r, capturerun.Event{Kind: capturerun.KindCaptureStart,
		Identity: "lab-r1", Type: "running-config"})
	rows := r.Rows()
	rows[0].Identity = "scribbled"
	if got := rowFor(t, r, "lab-r1", "running-config"); got.Identity != "lab-r1" {
		t.Error("mutating a returned row changed the run's own state")
	}
	notable := r.Decisions()
	_ = notable
	send(r, capturerun.Event{Kind: capturerun.KindNotApplic,
		Identity: "lab-r1", Type: "startup-config"})
	if len(notable) != 0 {
		t.Error("a previously returned Notable slice grew under the caller")
	}
}

// The decisions list is only worth reading if it stays short. Unchanged and
// stored are the run doing its job; a list that includes them is a log.
func TestNotableIsTheShortList(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindQueued, Identity: "lab-r1"},
		capturerun.Event{Kind: capturerun.KindConnected, Identity: "lab-r1"},
		capturerun.Event{Kind: capturerun.KindPlatform, Identity: "lab-r1", Platform: "cisco_ios"},
		capturerun.Event{Kind: capturerun.KindResolved, Identity: "lab-r1", Name: "lab-r1"},
		capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: "lab-r1", Type: "running-config"},
		capturerun.Event{Kind: capturerun.KindStored, Identity: "lab-r1", Type: "running-config", Bytes: 10},
		capturerun.Event{Kind: capturerun.KindUnchanged, Identity: "lab-r1", Type: "inventory"},
		capturerun.Event{Kind: capturerun.KindDeviceDone, Identity: "lab-r1"},
		// These four are the ones worth reading.
		capturerun.Event{Kind: capturerun.KindNotApplic, Identity: "lab-mx1", Type: "startup-config"},
		capturerun.Event{Kind: capturerun.KindNamed, Identity: "lab-r2", Name: "lab-r2"},
		capturerun.Event{Kind: capturerun.KindCaptureFail, Identity: "lab-r3", Type: "running-config",
			Err: errors.New("timed out")},
		capturerun.Event{Kind: capturerun.KindDeviceFail, Identity: "lab-r4",
			Err: errors.New("no route to host")},
	)
	got := r.Decisions()
	if len(got) != 4 {
		var kinds []string
		for _, ev := range got {
			kinds = append(kinds, ev.Kind.String())
		}
		t.Fatalf("Notable() has %d events %v, want the 4 out-of-the-ordinary ones", len(got), kinds)
	}
	for _, ev := range got {
		if ev.Describe() == "" {
			t.Errorf("%v renders as an empty line", ev.Kind)
		}
	}
}

// Run-wide counters and the decisions list must agree. An event that shows up
// in the list but not in its counter is a discrepancy a person will spend time
// on: three decisions listed, zero new host keys counted.
func TestRunWideCountersAgreeWithTheDecisionsList(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindHostKeyNew, Identity: "lab-r1",
			Detail: "ssh-ed25519 SHA256:aaa"},
		capturerun.Event{Kind: capturerun.KindAuthReject, Identity: "lab-r1",
			Detail: "lab-ro"},
		// Host-key and credential facts are about the run, not about a
		// device. A dialer that reports one without an identity — and
		// the callback it comes from takes a host string, not a device
		// — must still be counted, or the list and the counter
		// disagree.
		capturerun.Event{Kind: capturerun.KindHostKeyNew,
			Detail: "ssh-rsa SHA256:bbb"},
	)
	c := r.Counts()
	notable := r.Decisions()
	if len(notable) != 3 {
		t.Fatalf("Notable() has %d events, want 3", len(notable))
	}
	if c.NewHostKeys != 2 {
		t.Errorf("Counts.NewHostKeys = %d but 2 host-key events are in the decisions list", c.NewHostKeys)
	}
	if c.CredRejections != 1 {
		t.Errorf("Counts.CredRejections = %d, want 1", c.CredRejections)
	}
}

// Duration reads as elapsed-so-far while running and as the real span once
// settled, so a live table can show a column that ticks.
func TestDurationTicksWhileRunningAndFreezesWhenSettled(t *testing.T) {
	r := capturerun.New()
	send(r, capturerun.Event{Kind: capturerun.KindCaptureStart,
		Identity: "lab-r1", Type: "running-config"})

	first := rowFor(t, r, "lab-r1", "running-config").Duration()
	time.Sleep(5 * time.Millisecond)
	second := rowFor(t, r, "lab-r1", "running-config").Duration()
	if second <= first {
		t.Errorf("Duration did not advance while running: %v then %v", first, second)
	}

	send(r, capturerun.Event{Kind: capturerun.KindStored,
		Identity: "lab-r1", Type: "running-config", Bytes: 1})
	settled := rowFor(t, r, "lab-r1", "running-config").Duration()
	time.Sleep(5 * time.Millisecond)
	if again := rowFor(t, r, "lab-r1", "running-config").Duration(); again != settled {
		t.Errorf("Duration kept moving after the row settled: %v then %v", settled, again)
	}
}

// Elapsed does the same for the run as a whole.
func TestElapsedFreezesAtFinish(t *testing.T) {
	r := capturerun.New()
	time.Sleep(2 * time.Millisecond)
	if r.Elapsed() <= 0 {
		t.Error("Elapsed is not running before Finish")
	}
	r.Finish()
	at := r.Elapsed()
	time.Sleep(5 * time.Millisecond)
	if r.Elapsed() != at {
		t.Error("Elapsed kept moving after Finish")
	}
}

// OnChange is what a UI coalesces on. It must fire outside the lock, or the
// first handler that reads Rows() to decide whether to redraw deadlocks the
// engine that emitted the event.
func TestOnChangeFiresAndCanReadTheRun(t *testing.T) {
	r := capturerun.New()
	var (
		mu    sync.Mutex
		calls int
		rows  int
	)
	r.OnChange(func() {
		got := r.Rows() // would deadlock if OnChange ran under the lock
		mu.Lock()
		calls++
		rows = len(got)
		mu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		send(r, capturerun.Event{Kind: capturerun.KindCaptureStart,
			Identity: "lab-r1", Type: "running-config"})
		r.Finish()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnChange deadlocked against the run's own lock")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Errorf("OnChange fired %d times, want one per applied event plus Finish", calls)
	}
	if rows != 1 {
		t.Errorf("OnChange saw %d rows, want 1", rows)
	}
}

// The engine emits from every worker at once. Run's job is to make that safe,
// and this is the test that has to be run under -race to mean anything.
func TestConcurrentEmitIsSafeAndLosesNothing(t *testing.T) {
	r := capturerun.New()
	const devices, types = 20, 4
	typeNames := []string{"running-config", "startup-config", "inventory", "tech-support"}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for d := 0; d < devices; d++ {
		d := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "lab-r" + string(rune('a'+d))
			<-start
			send(r, capturerun.Event{Kind: capturerun.KindQueued, Identity: id})
			send(r, capturerun.Event{Kind: capturerun.KindPlatform, Identity: id,
				Platform: "cisco_ios"})
			for _, typ := range typeNames[:types] {
				send(r,
					capturerun.Event{Kind: capturerun.KindCaptureStart, Identity: id, Type: typ},
					capturerun.Event{Kind: capturerun.KindStored, Identity: id, Type: typ, Bytes: 10},
				)
			}
		}()
	}
	// A reader racing the writers, which is what a redraw ticker is.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.RowsSorted()
				_ = r.Counts()
				_ = r.Decisions()
			}
		}
	}()
	close(start)
	wg.Wait()
	close(stop)
	r.Finish()

	c := r.Counts()
	if c.Devices != devices {
		t.Errorf("Counts.Devices = %d, want %d", c.Devices, devices)
	}
	if c.Stored != devices*types {
		t.Errorf("Counts.Stored = %d, want %d", c.Stored, devices*types)
	}
	if c.BytesStored != devices*types*10 {
		t.Errorf("Counts.BytesStored = %d, want %d", c.BytesStored, devices*types*10)
	}
	if c.Running != 0 {
		t.Errorf("Counts.Running = %d after Finish, want 0", c.Running)
	}
	if got := len(r.Rows()); got != devices*types {
		t.Errorf("got %d rows, want %d", got, devices*types)
	}
}

// A nil Emit is the CLI's case: no event stream wanted, and the engine has no
// nil checks anywhere as a result.
func TestTheNilEmitIsAWorkingNoOp(t *testing.T) {
	var e capturerun.Emit
	e.Send(capturerun.Event{Kind: capturerun.KindStored, Identity: "lab-r1",
		Type: "running-config"})
}

// Send fills the timestamp when the caller did not, so nothing downstream has
// to guard against a zero time.
func TestSendFillsTheTimestamp(t *testing.T) {
	var got capturerun.Event
	e := capturerun.Emit(func(ev capturerun.Event) { got = ev })
	e.Send(capturerun.Event{Kind: capturerun.KindQueued, Identity: "lab-r1"})
	if got.At.IsZero() {
		t.Error("Send left At zero")
	}

	at := time.Now().Add(-time.Hour)
	e.Send(capturerun.Event{Kind: capturerun.KindQueued, Identity: "lab-r1", At: at})
	if !got.At.Equal(at) {
		t.Error("Send overwrote a timestamp the caller had already set")
	}
}

// An event with no identity cannot open a row — there is nothing to file it
// under — and must not invent a device record either.
func TestAnEventWithNoIdentityOpensNoRow(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindCaptureStart, Type: "running-config"},
		capturerun.Event{Kind: capturerun.KindStored, Type: "running-config", Bytes: 99},
	)
	if got := len(r.Rows()); got != 0 {
		t.Errorf("got %d rows from events with no identity, want 0", got)
	}
	if c := r.Counts(); c.Devices != 0 || c.Stored != 0 {
		t.Errorf("Counts = %+v, want an empty run", c)
	}
}

// A device-level event with no type opens no row. The engine sends several of
// these per device and a row per lifecycle event would triple the table.
func TestDeviceLevelEventsOpenNoRows(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindQueued, Identity: "lab-r1"},
		capturerun.Event{Kind: capturerun.KindConnected, Identity: "lab-r1"},
		capturerun.Event{Kind: capturerun.KindPlatform, Identity: "lab-r1", Platform: "cisco_ios"},
		capturerun.Event{Kind: capturerun.KindDeviceDone, Identity: "lab-r1"},
	)
	if got := len(r.Rows()); got != 0 {
		t.Errorf("got %d rows from device-level events alone, want 0", got)
	}
	if c := r.Counts(); c.Devices != 1 {
		t.Errorf("Counts.Devices = %d, want 1", c.Devices)
	}
}

// State.OK draws the line between "look at this" and "this is fine". Getting
// it wrong in either direction is a run summary that lies.
func TestStateOK(t *testing.T) {
	for _, tc := range []struct {
		state capturerun.State
		ok    bool
	}{
		{capturerun.StateStored, true},
		{capturerun.StateUnchanged, true},
		{capturerun.StateNotApplicable, true},
		{capturerun.StateFailed, false},
		{capturerun.StateRunning, false},
	} {
		if got := tc.state.OK(); got != tc.ok {
			t.Errorf("%v.OK() = %v, want %v", tc.state, got, tc.ok)
		}
		if tc.state.String() == "" {
			t.Errorf("%d has no string form", tc.state)
		}
	}
}

// Every Kind renders as something, since these strings reach a decisions pane.
func TestEveryKindHasAString(t *testing.T) {
	for k := capturerun.KindQueued; k <= capturerun.KindHostKeyNew; k++ {
		if s := k.String(); s == "" || s == "unknown" {
			t.Errorf("Kind(%d).String() = %q", int(k), s)
		}
	}
	if capturerun.KindUnknown.String() != "unknown" {
		t.Error("the zero Kind should render as unknown")
	}
}

// Pair is the row key and shows up in error text; it must read as one thing.
func TestPairString(t *testing.T) {
	p := capturerun.Pair{Identity: "lab-r1", Type: "running-config"}
	if p.String() != "lab-r1/running-config" {
		t.Errorf("Pair.String() = %q", p.String())
	}
}

// A device that could not be dialed owes one failed row per selected type, but
// the decisions list only owes one line. Thirteen dead devices with four types
// selected is fifty-two identical entries otherwise, and a summary nobody can
// read is a summary nobody reads.
func TestADeadDeviceIsReportedOnceInTheDecisionsList(t *testing.T) {
	r := capturerun.New()
	boom := errors.New("dial: no route to host")
	send(r, capturerun.Event{Kind: capturerun.KindDeviceFail, Identity: "lab-r1", Err: boom})
	for _, typ := range []string{"running-config", "startup-config", "inventory", "tech-support"} {
		send(r, capturerun.Event{Kind: capturerun.KindCaptureFail,
			Identity: "lab-r1", Type: typ, Err: boom})
	}
	r.Finish()

	if got := len(r.Rows()); got != 4 {
		t.Errorf("got %d rows, want one per selected type — the rows are what "+
			"keep an unreachable device distinguishable from one nobody asked about", got)
	}
	if got := r.Decisions(); len(got) != 1 {
		t.Errorf("the decisions list has %d entries for one dead device, want 1", len(got))
	}
	for _, row := range r.Rows() {
		if row.Detail == "" {
			t.Errorf("%s row lost its reason to the deduplication", row.Type)
		}
	}
}

// The deduplication must not swallow a capture failing on a device that is
// otherwise fine — that is precisely what the list exists to surface.
func TestACaptureFailureOnAHealthyDeviceIsStillNotable(t *testing.T) {
	r := capturerun.New()
	send(r,
		capturerun.Event{Kind: capturerun.KindPlatform, Identity: "lab-r1", Platform: "cisco_ios"},
		capturerun.Event{Kind: capturerun.KindStored, Identity: "lab-r1",
			Type: "running-config", Bytes: 100},
		capturerun.Event{Kind: capturerun.KindCaptureFail, Identity: "lab-r1",
			Type: "tech-support", Err: errors.New("timed out")},
	)
	if got := r.Decisions(); len(got) != 1 {
		t.Fatalf("the decisions list has %d entries, want the one real failure", len(got))
	}
}
