// internal/capture/capture_test.go
//
// The engine against real SSH servers that behave like devices.
//
// Everything here runs through fakedev, so the assertions are about what
// actually went on the wire rather than about which methods were called. That
// distinction matters most for the read-only claim: Server.Asked() reports the
// commands a device received, which is evidence, where an allowlist check on
// the spec table is only an intention.
package capture_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/credres"
	"github.com/scottpeterman/omegacatqt/internal/dial"
	"github.com/scottpeterman/omegacatqt/internal/fakedev"
	"github.com/scottpeterman/omegacatqt/internal/netexec"
	"github.com/scottpeterman/omegacatqt/internal/sshcore"
)

// lab builds a device that answers the config and inventory commands.
func lab(name string) fakedev.Config {
	cfg := fakedev.IOS(name)
	cfg.Commands["show running-config"] = "!\nhostname " + name + "\n!\ninterface Loopback0\n ip address 172.16.0.1 255.255.255.255\n"
	cfg.Commands["show startup-config"] = "!\nhostname " + name + "\n"
	cfg.Commands["show inventory"] = `NAME: "chassis", DESCR: "lab router"` + "\nPID: LAB-2911, SN: LAB0000001\n"
	return cfg
}

func dialerFor(t *testing.T, servers map[string]*fakedev.Server) dial.Func {
	t.Helper()
	return func(ctx context.Context, tgt dial.Target) (*sshcore.Client, error) {
		srv, ok := servers[tgt.Target]
		if !ok {
			return nil, errors.New("no such device: " + tgt.Target)
		}
		return srv.Dial("lab", "lab")
	}
}

func start(t *testing.T, cfg fakedev.Config) *fakedev.Server {
	t.Helper()
	srv, err := fakedev.Start(cfg)
	if err != nil {
		t.Fatalf("start device: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func engine(t *testing.T, cfg capture.Config) (*capture.Engine, *capture.FileStore) {
	t.Helper()
	st, err := capture.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	cfg.Store = st
	if cfg.SessionOpts.CommandTimeout == 0 {
		cfg.SessionOpts.CommandTimeout = 5 * time.Second
	}
	if cfg.SessionOpts.PagingDisable == "" {
		cfg.SessionOpts.PagingDisable = "terminal length 0"
	}
	e, err := capture.New(cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e, st
}

// withEmit builds an engine with an event sink attached. Separate from
// engine() because Config is consumed at New, so the emit has to be present
// before the first event rather than installed afterwards.
func withEmit(t *testing.T, cfg capture.Config, run *capturerun.Run) *capture.Engine {
	t.Helper()
	cfg.Emit = run.Emit()
	e, _ := engine(t, cfg)
	return e
}

func TestCaptureStoresWhatTheDeviceSaid(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	e, st := engine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs: []capture.Spec{capture.RunningConfig},
	})

	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].Err != nil {
		t.Fatalf("capture failed: %v", res[0].Err)
	}
	if res[0].Device != "lab-r1" {
		t.Errorf("stored under %q; the prompt says lab-r1", res[0].Device)
	}
	h, err := st.History("lab-r1", "running-config")
	if err != nil || len(h) != 1 {
		t.Fatalf("history = %d, %v", len(h), err)
	}
	if h[0].Command != "show running-config" {
		t.Errorf("history records command %q", h[0].Command)
	}
}

// The read-only claim, proved rather than asserted: the device reports every
// command it received, and the set must be the fingerprint probes plus the
// spec's command and nothing else.
func TestNothingButFingerprintProbesAndSpecCommandsReachTheDevice(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	e, _ := engine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs: []capture.Spec{capture.RunningConfig, capture.Inventory},
	})
	e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})

	permitted := map[string]bool{
		"terminal length 0":   true,
		"show version":        true,
		"show running-config": true,
		"show inventory":      true,
	}
	for _, cmd := range srv.Asked() {
		if !permitted[cmd] {
			t.Errorf("device received %q, which no spec asked for", cmd)
		}
	}
	if len(srv.Asked()) == 0 {
		t.Fatal("no commands recorded; the test proves nothing")
	}
}

// One visit per device, however many capture types are selected. A login per
// capture type would be three logins for a routine backup and would show up in
// a device's auth log as something worth investigating.
func TestOneLoginRegardlessOfHowManyCaptureTypes(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	e, _ := engine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs: []capture.Spec{capture.RunningConfig, capture.StartupConfig, capture.Inventory},
	})
	e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})

	if got := srv.Sessions(); got != 1 {
		t.Errorf("device saw %d logins for 3 capture types; want 1", got)
	}
}

// The third outcome. A platform with no command for a type is not a failure,
// and the distinction has to survive all the way to the Result.
func TestNotApplicableIsNotAFailure(t *testing.T) {
	cfg := fakedev.Junos("lab-mx1")
	cfg.Commands["show configuration | display set"] = "set system host-name lab-mx1\n"
	srv := start(t, cfg)

	e, _ := engine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-mx1": srv}),
		Specs: []capture.Spec{capture.RunningConfig, capture.StartupConfig},
		SessionOpts: netexec.Options{
			CommandTimeout: 5 * time.Second,
			PagingDisable:  "set cli screen-length 0",
		},
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-mx1"}})

	var startup, running capture.Result
	for _, r := range res {
		switch r.Type {
		case "startup-config":
			startup = r
		case "running-config":
			running = r
		}
	}
	if !startup.NotApplicable {
		t.Errorf("junos startup-config = %+v; junos has no startup/running split", startup)
	}
	if startup.Err != nil {
		t.Errorf("not-applicable carried an error: %v", startup.Err)
	}
	if running.Err != nil || running.NotApplicable {
		t.Errorf("junos running-config should have been captured: %+v", running)
	}
	// And the command must never have gone on the wire.
	for _, cmd := range srv.Asked() {
		if strings.Contains(cmd, "startup") {
			t.Errorf("a not-applicable capture still sent %q", cmd)
		}
	}
}

// A device that cannot be reached produces one result per selected type, not
// one device-level error. The unit of the run is the pair, so reconciling what
// was asked against what came back must not need a special case.
func TestAnUnreachableDeviceStillProducesOneResultPerType(t *testing.T) {
	e, _ := engine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{}),
		Specs: []capture.Spec{capture.RunningConfig, capture.Inventory},
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-r99"}})
	if len(res) != 2 {
		t.Fatalf("got %d results for 2 types, want 2", len(res))
	}
	for _, r := range res {
		if r.Err == nil {
			t.Errorf("%s reported success against an unreachable device", r.Type)
		}
	}
}

// The binding store decides the storage key. A device dialed by address whose
// binding says otherwise must file under the canonical name, or an
// address-seeded run and a name-seeded run build two config histories for one
// box.
func TestStorageKeyComesFromTheBindingStore(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	b := credres.NewMemoryBindings()
	if err := b.Bind("lab-r1.lab.example", "172.16.1.2", "lab-r1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	e, st := engine(t, capture.Config{
		Dial:     dialerFor(t, map[string]*fakedev.Server{"172.16.1.2": srv}),
		Specs:    []capture.Spec{capture.RunningConfig},
		Bindings: b,
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "172.16.1.2"}})
	if res[0].Err != nil {
		t.Fatalf("capture: %v", res[0].Err)
	}
	if res[0].Device != "lab-r1.lab.example" {
		t.Errorf("stored under %q, want the canonical name from the binding store", res[0].Device)
	}
	if h, _ := st.History("lab-r1.lab.example", "running-config"); len(h) != 1 {
		t.Errorf("nothing filed under the canonical name")
	}
}

// Capture contributes to identity. The common case is a device list nothing
// has ever crawled, so a read-only engine would file every one of those under
// whatever string the list used.
func TestAnUnknownDeviceIsNamedFromItsPromptAndBound(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	b := credres.NewMemoryBindings()
	e, _ := engine(t, capture.Config{
		Dial:     dialerFor(t, map[string]*fakedev.Server{"172.16.1.2": srv}),
		Specs:    []capture.Spec{capture.RunningConfig},
		Bindings: b,
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "172.16.1.2"}})
	if res[0].Device != "lab-r1" {
		t.Fatalf("stored under %q, want the name from the device's prompt", res[0].Device)
	}
	if got, ok := b.Resolve("172.16.1.2"); !ok || got.Canonical != "lab-r1" {
		t.Errorf("the address was not bound to the name: %+v, %v", got, ok)
	}
}

// Cheap before expensive, so a wedged tech-support cannot be the reason a
// config was never collected from a session the engine already had.
func TestCheapCapturesRunBeforeExpensiveOnes(t *testing.T) {
	cfg := lab("lab-r1")
	cfg.Commands["show tech-support"] = "start of tech\n"
	cfg.Hang = []string{"show tech-support"}
	srv := start(t, cfg)

	// A local expensive spec rather than capture.TechSupport: the builtin
	// declares a 15-minute Timeout, and the spec's own bound correctly
	// wins over the session default — so using it here would make this a
	// fifteen-minute test rather than a fast one.
	slowTech := capture.Spec{
		Type:        "tech-support",
		Description: "wedged on purpose",
		Commands: map[string]capture.Command{
			"cisco_ios": {Command: "show tech-support", Cost: capture.CostExpensive,
				Timeout: 1500 * time.Millisecond},
		},
	}
	e, st := engine(t, capture.Config{
		Dial: dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		// Deliberately listed worst-first: the engine must reorder.
		Specs:       []capture.Spec{slowTech, capture.RunningConfig},
		SessionOpts: netexec.Options{CommandTimeout: 5 * time.Second, PagingDisable: "terminal length 0"},
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})

	var tech, cfgRes capture.Result
	for _, r := range res {
		if r.Type == "tech-support" {
			tech = r
		} else {
			cfgRes = r
		}
	}
	if cfgRes.Err != nil {
		t.Errorf("the cheap capture was lost to the expensive one: %v", cfgRes.Err)
	}
	if tech.Err == nil {
		t.Error("the wedged command reported success")
	}
	if h, _ := st.History("lab-r1", "running-config"); len(h) != 1 {
		t.Error("the config was never stored")
	}
}

// A command whose output blows its declared bound fails that capture and
// leaves the session usable for the next one.
func TestAnOverLargeCaptureFailsOnlyItself(t *testing.T) {
	cfg := lab("lab-r1")
	cfg.Flood = map[string]int{"show inventory": 512 * 1024}
	srv := start(t, cfg)

	small := capture.Inventory
	small.Commands = map[string]capture.Command{
		"cisco_ios": {Command: "show inventory", MaxBytes: 16 * 1024},
	}
	e, st := engine(t, capture.Config{
		Dial:        dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs:       []capture.Spec{small, capture.RunningConfig},
		SessionOpts: netexec.Options{CommandTimeout: 20 * time.Second, PagingDisable: "terminal length 0"},
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})

	var inv, cfgRes capture.Result
	for _, r := range res {
		if r.Type == "inventory" {
			inv = r
		} else {
			cfgRes = r
		}
	}
	if !errors.Is(inv.Err, netexec.ErrOutputTooLarge) {
		t.Errorf("inventory err = %v, want ErrOutputTooLarge", inv.Err)
	}
	if cfgRes.Err != nil {
		t.Errorf("the over-large capture cost the session: %v", cfgRes.Err)
	}
	if h, _ := st.History("lab-r1", "inventory"); len(h) != 0 {
		t.Error("a truncated capture was stored anyway")
	}
	if h, _ := st.History("lab-r1", "running-config"); len(h) != 1 {
		t.Error("the following capture did not run")
	}
}

// Every pair reaches a terminal state, including for a device that was never
// reached. The crawl UI shipped with rows stuck on "running" because success
// had no emit beside it; the mirror of that bug is a device with no rows at
// all, which reads as "never asked for" rather than "asked for and
// unreachable".
func TestEveryPairGetsExactlyOneRowAndARealResult(t *testing.T) {
	good := start(t, lab("lab-r1"))
	e, _ := engine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-r1": good}),
		Specs: []capture.Spec{capture.RunningConfig, capture.Inventory},
	})
	run := capturerun.New()
	e = withEmit(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-r1": good}),
		Specs: []capture.Spec{capture.RunningConfig, capture.Inventory},
	}, run)

	res := e.Capture(context.Background(), []capture.Device{
		{Target: "lab-r1"}, {Target: "lab-r99"},
	})
	run.Finish()

	if len(res) != 4 {
		t.Fatalf("got %d results for 2 devices x 2 types, want 4", len(res))
	}
	rows := run.Rows()
	if len(rows) != 4 {
		t.Fatalf("run model has %d rows, want 4: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.State == capturerun.StateRunning {
			t.Errorf("%s/%s left running", r.Identity, r.Type)
		}
		if strings.Contains(r.Detail, "missing emit") {
			t.Errorf("%s/%s settled by the sweep, not by an event: %s", r.Identity, r.Type, r.Detail)
		}
	}
	c := run.Counts()
	if c.Stored != 2 || c.Failed != 2 || c.Devices != 2 || c.DevicesFailed != 1 {
		t.Errorf("counts = %+v; want 2 stored, 2 failed, 2 devices, 1 device failed", c)
	}
}

// An unchanged capture is a success and must be counted as one, not folded
// into "stored" and not treated as nothing having happened.
func TestASecondIdenticalCaptureReportsUnchanged(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	st, err := capture.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	mk := func(run *capturerun.Run) *capture.Engine {
		e, err := capture.New(capture.Config{
			Dial:        dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
			Store:       st,
			Specs:       []capture.Spec{capture.RunningConfig},
			Emit:        run.Emit(),
			SessionOpts: netexec.Options{CommandTimeout: 5 * time.Second, PagingDisable: "terminal length 0"},
		})
		if err != nil {
			t.Fatalf("new engine: %v", err)
		}
		return e
	}
	r1 := capturerun.New()
	mk(r1).Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})
	r1.Finish()

	r2 := capturerun.New()
	res := mk(r2).Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})
	r2.Finish()

	if !res[0].Artifact.Unchanged {
		t.Error("the second identical capture was not reported unchanged")
	}
	if c := r2.Counts(); c.Unchanged != 1 || c.Stored != 0 {
		t.Errorf("counts = %+v; want 1 unchanged, 0 stored", c)
	}
	if h, _ := st.History("lab-r1", "running-config"); len(h) != 2 {
		t.Errorf("history = %d entries; the skipped write must still be recorded", len(h))
	}
}

func TestCancelStopsTheRun(t *testing.T) {
	cfg := lab("lab-r1")
	cfg.Latency = 300 * time.Millisecond
	srv := start(t, cfg)

	e, _ := engine(t, capture.Config{
		Dial:        dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs:       []capture.Spec{capture.RunningConfig},
		Concurrency: 1,
		SessionOpts: netexec.Options{CommandTimeout: 30 * time.Second, PagingDisable: "terminal length 0"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	res := e.Capture(ctx, []capture.Device{{Target: "lab-r1"}, {Target: "lab-r1"}})
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a cancelled run took %s", d)
	}
	for _, r := range res {
		if r.Err == nil {
			t.Error("a cancelled run reported a successful capture")
		}
	}
}

func TestNewRejectsBadConfigurations(t *testing.T) {
	d := func(context.Context, dial.Target) (*sshcore.Client, error) { return nil, nil }
	st, _ := capture.OpenFileStore(t.TempDir())

	cases := []struct {
		name string
		cfg  capture.Config
	}{
		{"no dialer", capture.Config{Store: st, Specs: []capture.Spec{capture.RunningConfig}}},
		{"no store", capture.Config{Dial: d, Specs: []capture.Spec{capture.RunningConfig}}},
		{"no specs", capture.Config{Dial: d, Store: st}},
		{"duplicate spec", capture.Config{Dial: d, Store: st,
			Specs: []capture.Spec{capture.RunningConfig, capture.RunningConfig}}},
		{"invalid spec", capture.Config{Dial: d, Store: st,
			Specs: []capture.Spec{{Type: "../etc", Commands: map[string]capture.Command{"cisco_ios": {Command: "show version"}}}}}},
	}
	for _, c := range cases {
		if _, err := capture.New(c.cfg); err == nil {
			t.Errorf("%s: New accepted it", c.name)
		}
	}
}
