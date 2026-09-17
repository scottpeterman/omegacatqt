// internal/capturerun/demo.go
//
// A scripted run, for bringing the capture view up without a lab.
//
// Same reasoning as crawlrun.Demo: the first thing anyone does with a new view
// is fix toolkit mistakes, and doing that against real gear is a slow round
// trip through a lab that has to be reachable. This plays a realistic capture
// into a Run instead — every state the table can show, on a device mix that
// makes the interesting rows appear.
//
// It lives here rather than in the view package because it is data, it is
// testable, and keeping it out of the UI means the only unverified code in the
// preview path is the widget layer itself.
//
// # The trap this file is under standing orders to avoid
//
// crawlrun.Demo emitted KindReached while the crawler emitted nothing of the
// kind. Demo mode looked perfect and every device on the first real run was
// swept into a failure by Finish. The demo did not cause that bug, but it hid
// it for a day, because a preview built from a hand-written script proves what
// the script says and not what the engine does.
//
// So the script below emits only kinds the engine emits, and
// TestDemoEmitsNoKindTheEngineDoesNot in the capture package holds it to that
// by driving a real engine against fakedev and comparing the two sets.
package capturerun

import (
	"errors"
	"time"
)

// DemoOptions controls playback.
type DemoOptions struct {
	// Step is the pause between events. Zero plays instantly, which is
	// what tests want; something like 40ms looks like a run in progress.
	Step time.Duration

	// Stop aborts playback early, leaving pairs mid-flight so the view can
	// be checked against a cancelled run.
	Stop <-chan struct{}
}

// DemoTypes are the capture types the script selects, in the order a run would
// order them: cheap first.
var DemoTypes = []string{"running-config", "startup-config", "inventory"}

// DemoEvents returns the scripted run as data.
//
// Exported separately from Demo because the script is the thing worth
// inspecting: the guard in internal/capture compares the kinds it contains
// against the kinds a real engine emits, and it can only do that if the script
// is readable without being played into something.
//
// The device mix is chosen so that every state appears at least once without
// the run looking broken:
//
//   - two IOS boxes captured normally, one of them with a config that changed
//     and one unchanged, which is the split a nightly schedule actually has
//   - an Arista spine, so the platform column has more than one value
//   - a Junos box, which has no startup-config command and is therefore the
//     live not-applicable case rather than a fabricated one
//   - a device that never answers, which must produce a row per selected type
//     and not vanish from the table
//   - one capture that fails on a device that is otherwise fine, which is the
//     distinction the decisions list has to keep making
func DemoEvents() []Event {
	var script []Event
	send := func(ev Event) bool {
		script = append(script, ev)
		return true
	}

	type capture struct {
		typ       string
		command   string
		bytes     int
		sha       string
		unchanged bool
		notApplic bool
		fail      string
	}
	type device struct {
		identity string
		name     string
		platform string
		resolved bool // named by the binding store rather than the prompt
		newKey   bool
		failWhy  string
		captures []capture
	}

	devices := []device{
		{
			identity: "172.16.1.2", name: "wan-core-1.lab.local", platform: "cisco_ios",
			newKey: true,
			captures: []capture{
				{typ: "running-config", command: "show running-config", bytes: 8421, sha: "3f9a2c17"},
				{typ: "startup-config", command: "show startup-config", bytes: 8390, sha: "b1d40e88"},
				{typ: "inventory", command: "show inventory", bytes: 612, sha: "77c0aa31"},
			},
		},
		{
			identity: "usa-rtr-1.lab.local", name: "usa-rtr-1.lab.local", platform: "cisco_ios",
			resolved: true,
			captures: []capture{
				// The common outcome on a schedule, and the one the
				// counters must not make look like nothing happened.
				{typ: "running-config", command: "show running-config", bytes: 7104, sha: "c22f81ab", unchanged: true},
				{typ: "startup-config", command: "show startup-config", bytes: 7104, sha: "c22f81ab", unchanged: true},
				{typ: "inventory", command: "show inventory", bytes: 588, sha: "0ae37f52", unchanged: true},
			},
		},
		{
			identity: "eng-spine-1", name: "eng-spine-1", platform: "arista_eos",
			resolved: true,
			captures: []capture{
				{typ: "running-config", command: "show running-config", bytes: 4933, sha: "9e10bd44"},
				{typ: "startup-config", command: "show startup-config", bytes: 4901, sha: "45ff2210", unchanged: true},
				// A real capture failure on an otherwise healthy
				// device — the case the decisions list exists for,
				// and the one alreadySaid must NOT dedupe away.
				//
				// Deliberately NOT an unsettled row swept by Finish.
				// That produces "run ended with no result for this
				// capture (missing emit, not a timeout)", which is a
				// message about an ENGINE BUG. A preview that always
				// shows it teaches the reader that the string is
				// normal, and it is the opposite of normal. The
				// running state is demonstrated for free during
				// playback whenever Step is non-zero.
				{typ: "inventory", command: "show inventory",
					fail: "show inventory: no prompt within 60s"},
			},
		},
		{
			identity: "eng-fw-1.lab.local", name: "eng-fw-1.lab.local", platform: "juniper_junos",
			captures: []capture{
				{typ: "running-config", command: "show configuration", bytes: 3180, sha: "8c47de09"},
				// Junos has no startup-config. The third outcome, and
				// the one most often mistaken for a failure.
				{typ: "startup-config", notApplic: true},
				{typ: "inventory", command: "show chassis hardware", bytes: 420, sha: "1b9930cc"},
			},
		},
		{
			identity: "usa-leaf-3.lab.local", platform: "",
			failWhy: "dial tcp 172.16.128.43:22: connect: no route to host",
			captures: []capture{
				// A device that never answered still owes one row per
				// selected type, or an unreachable box is absent from
				// the table and looks like one nobody asked for.
				{typ: "running-config"},
				{typ: "startup-config"},
				{typ: "inventory"},
			},
		},
	}

	for _, d := range devices {
		if !send(Event{Kind: KindQueued, Identity: d.identity}) {
			return script
		}
		if d.newKey {
			if !send(Event{Kind: KindHostKeyNew, Identity: d.identity,
				Detail: "SHA256:0Xf2rQ7mJ4kd9pVn1sBcT6yLwE3uHgAz8QoRiKvNxMc"}) {
				return script
			}
		}
		if d.failWhy != "" {
			if !send(Event{Kind: KindDeviceFail, Identity: d.identity,
				Err: errors.New(d.failWhy)}) {
				return script
			}
			for _, c := range d.captures {
				if !send(Event{Kind: KindCaptureFail, Identity: d.identity,
					Type: c.typ, Err: errors.New(d.failWhy)}) {
					return script
				}
			}
			continue
		}

		if !send(Event{Kind: KindConnected, Identity: d.identity}) {
			return script
		}
		if d.resolved {
			if !send(Event{Kind: KindResolved, Identity: d.identity, Name: d.name,
				Detail: "binding store"}) {
				return script
			}
		} else {
			if !send(Event{Kind: KindNamed, Identity: d.identity, Name: d.name,
				Detail: "named from prompt"}) {
				return script
			}
		}
		if !send(Event{Kind: KindPlatform, Identity: d.identity, Platform: d.platform}) {
			return script
		}

		for _, c := range d.captures {
			if c.notApplic {
				if !send(Event{Kind: KindNotApplic, Identity: d.identity,
					Type: c.typ, Platform: d.platform}) {
					return script
				}
				continue
			}
			if !send(Event{Kind: KindCaptureStart, Identity: d.identity,
				Type: c.typ, Command: c.command, Platform: d.platform}) {
				return script
			}
			if c.fail != "" {
				if !send(Event{Kind: KindCaptureFail, Identity: d.identity,
					Type: c.typ, Command: c.command, Err: errors.New(c.fail)}) {
					return script
				}
				continue
			}
			kind := KindStored
			if c.unchanged {
				kind = KindUnchanged
			}
			if !send(Event{Kind: kind, Identity: d.identity, Type: c.typ,
				Command: c.command, Bytes: c.bytes, SHA: c.sha,
				Path: demoPath(d.name, c.typ)}) {
				return script
			}
		}
		if !send(Event{Kind: KindDeviceDone, Identity: d.identity}) {
			return script
		}
	}
	return script
}

// demoPath names a file that does not exist. The view uses Path only to decide
// whether a row can be opened and what to hand the store, so a plausible name
// exercises the enabled state; opening one in demo mode is expected to report
// that the file is missing, which is itself worth seeing once.
func demoPath(name, typ string) string {
	if name == "" {
		return ""
	}
	return "devices/" + name + "/" + typ + "/2026-08-01T02-15-00Z.txt"
}

// Demo plays the scripted run into run, pacing it with opts. It returns when
// the script ends or Stop fires; call it on a goroutine when Step is non-zero.
func Demo(run *Run, opts DemoOptions) {
	e := run.Emit()
	for _, ev := range DemoEvents() {
		if ev.At.IsZero() {
			ev.At = time.Now()
		}
		e.Send(ev)
		if opts.Step > 0 {
			select {
			case <-opts.Stop:
				return
			case <-time.After(opts.Step):
			}
		} else {
			select {
			case <-opts.Stop:
				return
			default:
			}
		}
	}
}
