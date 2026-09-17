// internal/capture/demo_guard_test.go
//
// The guard that stops demo mode from previewing a run the engine cannot
// produce.
//
// crawlrun.Demo emitted KindReached. The crawler emitted nothing of the kind,
// because the emit sites were wired by pairing each one with the log line
// beside it and plain success has no log line. So demo mode rendered a perfect
// table while the first real run swept every device into a failure — the demo
// did not cause that bug, it hid it, and it hid it precisely because a
// hand-written script proves what the script says rather than what the engine
// does.
//
// This drives a real engine against fakedev, collects the kinds it emits, and
// holds capturerun.Demo to that set. A demo that shows a state nothing can
// reach fails the build instead of costing a lab session.
//
// The comparison is one-directional on purpose. Demo ⊆ engine is the property
// worth enforcing; the reverse would fail every time the engine grows a kind
// the script has not been extended to show, which is a gap in a preview and
// not a defect in the product.
package capture_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/credres"
	"github.com/scottpeterman/omegacatqt/internal/fakedev"
)

// emittedOutsideTheEngine are kinds no engine run can produce because they are
// emitted by the dial layer instead.
//
// KindHostKeyNew comes from capturedial.HostKeyEmitter, wired into the
// dialer's first-contact callback — the engine is handed a dial.Func and never
// sees a host key. capturedial cannot be imported here without pulling vaultcli
// and the OS keyring into this package's test binary, which is the same
// reasoning that keeps Build out of internal/capture in the first place.
//
// This list is the two-place edit that buys that separation. A kind added here
// without a real emitter behind it defeats the guard, so it stays short and
// each entry names where it actually comes from.
var emittedOutsideTheEngine = map[capturerun.Kind]string{
	capturerun.KindHostKeyNew: "capturedial.HostKeyEmitter",
}

// recorder collects the distinct kinds seen on an event stream.
//
// Locked because the engine emits from every worker goroutine — the same
// reason capturerun.Run holds a mutex. An unlocked map here passes reliably
// without -race and fails under it, which is the worst of both.
type recorder struct {
	mu    sync.Mutex
	kinds map[capturerun.Kind]int
}

func newRecorder() *recorder { return &recorder{kinds: map[capturerun.Kind]int{}} }

func (r *recorder) emit() capturerun.Emit {
	return func(ev capturerun.Event) {
		r.mu.Lock()
		r.kinds[ev.Kind]++
		r.mu.Unlock()
	}
}

func (r *recorder) saw(k capturerun.Kind) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.kinds[k] > 0
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for k := range r.kinds {
		out = append(out, k.String())
	}
	sort.Strings(out)
	return out
}

// engineKinds runs a capture that reaches every outcome the engine has, and
// returns the kinds it emitted.
//
// Under-driving this helper is the way this guard goes wrong: it fails on a
// perfectly good demo because the engine was never put in a position to emit
// the kind in question. Two of the five kinds here need setting up for
// specifically — a binding has to exist before KindResolved is reachable, and
// the same content has to be captured twice before KindUnchanged is.
func engineKinds(t *testing.T) *recorder {
	t.Helper()

	// An IOS box that answers everything, a Junos box that has no
	// startup-config command (the not-applicable path), and a device with
	// no server behind it (the dial failure path).
	ios := start(t, lab("lab-r1"))
	junos := start(t, fakedev.Junos("lab-fw-1"))

	// A binding for one device and not the other, so both naming paths run:
	// the bound box resolves, the unbound one is named from its prompt.
	b := credres.NewMemoryBindings()
	if err := b.Bind("lab-r1.lab.example", "lab-r1"); err != nil {
		t.Fatalf("bind: %v", err)
	}

	rec := newRecorder()
	e, _ := engine(t, capture.Config{
		Dial: dialerFor(t, map[string]*fakedev.Server{
			"lab-r1":   ios,
			"lab-fw-1": junos,
		}),
		Specs: []capture.Spec{
			capture.RunningConfig,
			capture.StartupConfig,
			capture.Inventory,
		},
		Bindings: b,
		Emit:     rec.emit(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	devices := []capture.Device{
		{Target: "lab-r1"},
		{Target: "lab-fw-1"},
		{Target: "lab-gone-1"}, // nothing listening; the device-failure path
	}
	e.Capture(ctx, devices)
	// Again, against the same store and the same unchanged devices. This is
	// the only way KindUnchanged happens, and it is the outcome a healthy
	// schedule produces almost every night.
	e.Capture(ctx, devices)
	return rec
}

func TestDemoEmitsNoKindTheEngineDoesNot(t *testing.T) {
	engineSeen := engineKinds(t)

	demoSeen := map[capturerun.Kind]int{}
	for _, ev := range capturerun.DemoEvents() {
		demoSeen[ev.Kind]++
	}
	if len(demoSeen) == 0 {
		t.Fatal("DemoEvents is empty; the guard would pass vacuously")
	}

	for k := range demoSeen {
		if engineSeen.saw(k) {
			continue
		}
		if where, ok := emittedOutsideTheEngine[k]; ok {
			t.Logf("%s comes from %s, not the engine", k, where)
			continue
		}
		t.Errorf("capturerun.Demo emits %s and nothing does at runtime — "+
			"demo mode would preview a state a real capture can never reach "+
			"(engine emits: %v)", k, engineSeen.names())
	}
}

// TestDemoReachesEveryTerminalState is the other half: a preview whose table
// never shows unchanged, or never shows not-applicable, does not exercise the
// styling decisions those states exist to force.
func TestDemoReachesEveryTerminalState(t *testing.T) {
	run := capturerun.New()
	capturerun.Demo(run, capturerun.DemoOptions{})

	// Every pair settles on its own. Nothing is left for Finish to sweep,
	// on purpose: a swept row renders as "run ended with no result for this
	// capture (missing emit, not a timeout)", which is a message about an
	// engine bug and has no business being the preview's normal output.
	seen := map[capturerun.State]int{}
	for _, row := range run.Rows() {
		seen[row.State]++
	}
	if seen[capturerun.StateRunning] != 0 {
		t.Errorf("%d row(s) left running at the end of the script; Finish would "+
			"sweep them into a missing-emit failure and the preview would "+
			"permanently display an engine-bug message",
			seen[capturerun.StateRunning])
	}
	run.Finish()

	seen = map[capturerun.State]int{}
	for _, row := range run.Rows() {
		seen[row.State]++
	}
	for _, s := range []capturerun.State{
		capturerun.StateStored,
		capturerun.StateUnchanged,
		capturerun.StateNotApplicable,
		capturerun.StateFailed,
	} {
		if seen[s] == 0 {
			t.Errorf("no row in state %q; the preview cannot show how it renders", s)
		}
	}
	if seen[capturerun.StateRunning] != 0 {
		t.Error("a row survived Finish as running")
	}

	// The unreachable device must own a row per selected type rather than
	// vanishing — the bug the engine already had once.
	byDevice := map[string]int{}
	for _, row := range run.Rows() {
		byDevice[row.Identity]++
	}
	if got := byDevice["usa-leaf-3.lab.local"]; got != len(capturerun.DemoTypes) {
		t.Errorf("the unreachable device has %d rows, want %d (one per selected type)",
			got, len(capturerun.DemoTypes))
	}
}
