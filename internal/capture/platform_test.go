// internal/capture/platform_test.go
//
// Where a device's platform comes from, and what each source may do.
//
// A platform set on a device by a person is the authority; a crawl's guess is
// a hint that is only ever compared. The test that matters most here is the
// paging one: a set platform replaces classification, but the paging-disable
// command that classification used to send has to survive the replacement, or
// the first long command waits at --More-- until it times out. The fake device
// actually pages, so that is proved on the wire rather than assumed.
package capture_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/dial"
	"github.com/scottpeterman/omegacatqt/internal/fakedev"
	"github.com/scottpeterman/omegacatqt/internal/sshcore"
)

// bareEngine is engine() without its test default of a session-level paging
// command, which would disable paging before fingerprinting ever ran and hide
// the very thing these tests are about. Production sends no session-level
// paging command either: capturedial leaves it empty.
func bareEngine(t *testing.T, cfg capture.Config) (*capture.Engine, *capture.FileStore) {
	t.Helper()
	st, err := capture.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	cfg.Store = st
	cfg.SessionOpts.CommandTimeout = 2 * time.Second
	e, err := capture.New(cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e, st
}

// A Junos box that pages. Classifying it would send "terminal length 0" first
// (rejected) and then the Junos probe; a set platform sends the Junos paging
// command straight away and nothing from any other family.
func pagingJunos(name string) fakedev.Config {
	cfg := fakedev.Junos(name)
	cfg.Pager, cfg.PageLines = "set cli screen-length 0", 3
	cfg.Commands["show configuration | display set"] = strings.Join([]string{
		"set version 21.4R3-S5.4",
		"set system host-name " + name,
		"set system services ssh",
		"set interfaces ge-0/0/0 unit 0 family inet address 172.16.2.1/30",
		"set interfaces lo0 unit 0 family inet address 172.16.0.9/32",
		"set routing-options router-id 172.16.0.9",
	}, "\n")
	return cfg
}

func TestASetPlatformStillDisablesPagingAndCapturesALongConfig(t *testing.T) {
	srv := start(t, pagingJunos("lab-mx1"))
	e, st := bareEngine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-mx1": srv}),
		Specs: []capture.Spec{capture.RunningConfig},
	})

	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-mx1", Platform: "juniper_junos"}})
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("capture: %+v", res)
	}
	if res[0].Platform != "juniper_junos" {
		t.Errorf("platform %q, want the one set", res[0].Platform)
	}
	h, err := st.History(res[0].Device, "running-config")
	if err != nil || len(h) != 1 {
		t.Fatalf("history = %v, %v", h, err)
	}

	want := []string{"set cli screen-length 0", "show version", "show configuration | display set"}
	if got := srv.Asked(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("device received %q\nwant exactly %q: the platform's own paging and version commands, no other family's probes", got, want)
	}
}

// The control for the test above: the same device, detected rather than set,
// also captures. If this one failed too the fixture would be what is broken.
func TestADetectedPlatformCapturesThePagingDeviceToo(t *testing.T) {
	srv := start(t, pagingJunos("lab-mx1"))
	e, _ := bareEngine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-mx1": srv}),
		Specs: []capture.Spec{capture.RunningConfig},
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-mx1"}})
	if len(res) != 1 || res[0].Err != nil || res[0].Platform != "juniper_junos" {
		t.Fatalf("capture: %+v", res)
	}
}

// platformEvent runs one device and returns its platform event.
func platformEvent(t *testing.T, cfg fakedev.Config, d capture.Device, specs ...capture.Spec) (capturerun.Event, []capture.Result, *capturerun.Run) {
	t.Helper()
	srv := start(t, cfg)
	run := capturerun.New()
	toRun := run.Emit()
	var platform capturerun.Event
	e, _ := bareEngine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{d.Target: srv}),
		Specs: specs,
		Emit: func(ev capturerun.Event) {
			if ev.Kind == capturerun.KindPlatform {
				platform = ev
			}
			toRun(ev)
		},
	})
	res := e.Capture(context.Background(), []capture.Device{d})
	run.Finish()
	return platform, res, run
}

func notable(run *capturerun.Run, kind capturerun.Kind) []capturerun.Event {
	var out []capturerun.Event
	for _, ev := range run.Decisions() {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// Set wins over what the device says, and the disagreement is a decision.
func TestASetPlatformWinsAndTheDisagreementIsReported(t *testing.T) {
	cfg := fakedev.EOS("lab-spine-1")
	cfg.Commands["show running-config"] = "! eos config\nhostname lab-spine-1\n"
	ev, res, run := platformEvent(t, cfg, capture.Device{Target: "lab-spine-1", Platform: "cisco_ios"}, capture.RunningConfig)

	if ev.Platform != "cisco_ios" || res[0].Platform != "cisco_ios" {
		t.Errorf("event %q, result %q: the set platform must win", ev.Platform, res[0].Platform)
	}
	if !strings.Contains(ev.Detail, "the device reports arista_eos") {
		t.Errorf("detail %q does not name what the device reports", ev.Detail)
	}
	if len(notable(run, capturerun.KindPlatform)) != 1 {
		t.Error("a set platform the device disagrees with is not in the decisions list")
	}
}

// A set platform the device agrees with says nothing.
func TestASetPlatformThatAgreesIsQuiet(t *testing.T) {
	cfg := fakedev.EOS("lab-spine-1")
	cfg.Commands["show running-config"] = "! eos config\nhostname lab-spine-1\n"
	ev, res, run := platformEvent(t, cfg, capture.Device{Target: "lab-spine-1", Platform: "arista_eos"}, capture.RunningConfig)
	if res[0].Err != nil || ev.Detail != "" || len(notable(run, capturerun.KindPlatform)) != 0 {
		t.Errorf("err %v, detail %q: an agreeing platform should be silent", res[0].Err, ev.Detail)
	}
}

// A platform name this build does not know fails the device with the name in
// the message, before any capture command is sent.
func TestAnUnknownSetPlatformFailsTheDeviceByName(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	e, _ := bareEngine(t, capture.Config{
		Dial:  dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs: []capture.Spec{capture.RunningConfig},
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1", Platform: "cisco_ioss"}})
	if len(res) != 1 || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), `"cisco_ioss"`) {
		t.Fatalf("want a failure naming the platform, got %+v", res)
	}
	for _, cmd := range srv.Asked() {
		if cmd == "show running-config" {
			t.Error("a capture command was sent for a device whose platform was refused")
		}
	}
}

// The inventory's guess is compared, never used.
func TestTheInventoryHintIsReportedNotUsed(t *testing.T) {
	cfg := fakedev.EOS("lab-spine-1")
	cfg.Commands["show running-config"] = "! eos config\nhostname lab-spine-1\n"

	ev, res, run := platformEvent(t, cfg, capture.Device{Target: "lab-spine-1", PlatformHint: "cisco_ios"}, capture.RunningConfig)
	if ev.Platform != "arista_eos" || res[0].Platform != "arista_eos" {
		t.Errorf("platform %q / %q: a hint must not override detection", ev.Platform, res[0].Platform)
	}
	if ev.Detail != "inventory says cisco_ios, detected arista_eos" || len(notable(run, capturerun.KindPlatform)) != 1 {
		t.Errorf("detail %q: want the mismatch reported as a decision", ev.Detail)
	}

	ev, _, run = platformEvent(t, cfg, capture.Device{Target: "lab-spine-1", PlatformHint: "arista_eos"}, capture.RunningConfig)
	if ev.Detail != "" || len(notable(run, capturerun.KindPlatform)) != 0 {
		t.Errorf("detail %q: a hint that matches should be silent", ev.Detail)
	}
}

// A device capture cannot classify, whose inventory entry has a guess, is told
// how to use it.
func TestAnUndetectedDeviceWithAHintSaysHowToUseIt(t *testing.T) {
	cfg := fakedev.IOS("lab-odd1")
	cfg.Commands["show version"] = "Some Vendor NOS 1.0"
	ev, _, _ := platformEvent(t, cfg, capture.Device{Target: "lab-odd1", PlatformHint: "cisco_ios"}, capture.RunningConfig)
	if ev.Platform != "unknown" || !strings.Contains(ev.Detail, "set the platform on the device") {
		t.Errorf("platform %q detail %q", ev.Platform, ev.Detail)
	}
}

// Per-device legacy reaches the dialer.
func TestPerDeviceLegacyReachesTheDialer(t *testing.T) {
	srv := start(t, lab("lab-r1"))
	var got []bool
	e, _ := bareEngine(t, capture.Config{
		Dial: func(ctx context.Context, tgt dial.Target) (*sshcore.Client, error) {
			got = append(got, tgt.Legacy)
			if tgt.Target != "lab-r1" {
				return nil, errors.New("no such device")
			}
			return srv.Dial("lab", "lab")
		},
		Specs: []capture.Spec{capture.RunningConfig},
	})
	e.Capture(context.Background(), []capture.Device{{Target: "lab-r1", Legacy: true}})
	if len(got) != 1 || !got[0] {
		t.Fatalf("dialer saw Legacy %v, want [true]", got)
	}
}
