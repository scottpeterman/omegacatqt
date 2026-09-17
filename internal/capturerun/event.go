// internal/capturerun/event.go
//
// Structured capture events.
//
// Written alongside the engine rather than fitted to it afterwards. crawlrun
// arrived late and paid for it: terminal events keyed on Hostname while queue
// events keyed on identity, so with a domain suffix set one device produced
// two rows and nobody noticed until a table made it visible.
//
// The lesson taken from that is not "emit more" — it is that the key has to be
// decided once, at the start, and nothing may key on anything else. Here the
// key is a Pair: the device's claim identity and the capture type. Both halves
// are required on every event that concerns a capture, and Identity is the
// claim key rather than any display name, for the same reason it is in
// crawlrun.
package capturerun

import (
	"fmt"
	"time"
)

// Kind is what happened.
type Kind int

const (
	KindUnknown Kind = iota

	// Lifecycle, per device.
	KindQueued     // admitted to the run
	KindConnected  // session open
	KindPlatform   // fingerprinted
	KindDeviceDone // every capture for this device has settled
	KindDeviceFail // the device could not be reached or fingerprinted

	// Lifecycle, per (device, capture type).
	KindCaptureStart // the command went on the wire
	KindStored       // new content written
	KindUnchanged    // content matched the previous capture; no file written
	KindNotApplic    // this platform has no command for this type
	KindCaptureFail  // the command or the store failed

	// Identity.
	KindResolved // the binding store supplied the canonical name
	KindNamed    // no binding existed; named from the device's own prompt and bound

	// Credentials and host keys, mirroring crawlrun.
	KindAuthOK
	KindAuthReject
	KindCredParked
	KindHostKeyNew
)

func (k Kind) String() string {
	switch k {
	case KindQueued:
		return "queued"
	case KindConnected:
		return "connected"
	case KindPlatform:
		return "platform"
	case KindDeviceDone:
		return "device-done"
	case KindDeviceFail:
		return "device-failed"
	case KindCaptureStart:
		return "capture-start"
	case KindStored:
		return "stored"
	case KindUnchanged:
		return "unchanged"
	case KindNotApplic:
		return "not-applicable"
	case KindCaptureFail:
		return "capture-failed"
	case KindResolved:
		return "resolved"
	case KindNamed:
		return "named"
	case KindAuthOK:
		return "auth-ok"
	case KindAuthReject:
		return "auth-reject"
	case KindCredParked:
		return "cred-parked"
	case KindHostKeyNew:
		return "host-key-new"
	}
	return "unknown"
}

// Pair is the row key: one device, one capture type.
//
// The type half is what separates this from crawlrun. A capture run is not a
// list of devices that either worked or did not — a device can hand over its
// config, have no startup-config to give, and time out on tech-support, all in
// one visit. Collapsing that to one device row throws away which of the three
// is the one worth acting on.
type Pair struct {
	Identity string
	Type     string
}

func (p Pair) String() string { return p.Identity + "/" + p.Type }

// Event is one thing the capture engine decided or observed.
//
// Type is empty on device-level events (dial, fingerprint), set on
// capture-level ones. A consumer keys device state on Identity and capture
// state on Pair; nothing keys on Name, which arrives late and can change.
type Event struct {
	At       time.Time
	Kind     Kind
	Identity string
	Type     string

	// Name is the device's canonical name once known. Empty until the
	// binding store answers or the prompt does.
	Name string

	// Platform is the fingerprint result.
	Platform string

	// Command is what went on the wire, for capture-level events.
	Command string

	// Bytes and SHA are the stored artifact's size and digest.
	Bytes int
	SHA   string

	// Path is the stored file. Carried on the event rather than rebuilt by
	// the consumer: the layout is the store's business, and a view that
	// derives a path from a device name and a type has quietly taken a
	// second opinion on the slug rule.
	//
	// On an unchanged capture this is the existing file that matched.
	Path string

	// Detail carries the per-event fact worth reading: a failure reason, a
	// resolution note, a credential name.
	Detail string

	// Err is the failure, when there is one.
	Err error

	// Parse outcome, on a stored or unchanged event for a type that is
	// parsed. ParseStatus is empty when the output was not parsed at all;
	// otherwise "parsed" or "no-match", with the template that scored best
	// and its score either way.
	ParseStatus  string
	Template     string
	ParseScore   float64
	ParseRecords int

	// Seq is the run's sequence number for this event, stamped by Run as it
	// applies it. Zero on an event that has not been through a Run.
	Seq uint64
}

// Pair returns the row key this event belongs to.
func (e Event) Pair() Pair { return Pair{Identity: e.Identity, Type: e.Type} }

// Notable reports whether an event belongs in a decisions list — the short
// view of a run that is worth reading rather than scrolling.
//
// Unchanged is deliberately NOT notable. It is the expected outcome of a
// healthy backup schedule and would drown everything else. Not-applicable is
// notable exactly once per platform in practice, and it is the event most
// likely to be mistaken for a failure, so it earns its place.
func (e Event) Notable() bool {
	switch e.Kind {
	case KindDeviceFail, KindCaptureFail, KindNotApplic,
		KindNamed, KindAuthReject, KindCredParked, KindHostKeyNew:
		return true
	case KindPlatform:
		// Only with something to say: the platform was set and the device
		// disagrees, or the inventory's guess was wrong.
		return e.Detail != ""
	}
	return false
}

// Describe renders one event as a line.
func (e Event) Describe() string {
	who := e.Identity
	if e.Type != "" {
		who = e.Pair().String()
	}
	switch e.Kind {
	case KindStored:
		return fmt.Sprintf("%s: stored %d bytes", who, e.Bytes)
	case KindUnchanged:
		return fmt.Sprintf("%s: unchanged", who)
	case KindNotApplic:
		if e.Detail != "" {
			return fmt.Sprintf("%s: not applicable on %s: %s", who, e.Platform, e.Detail)
		}
		return fmt.Sprintf("%s: not applicable on %s", who, e.Platform)
	case KindCaptureFail, KindDeviceFail:
		if e.Err != nil {
			return fmt.Sprintf("%s: %v", who, e.Err)
		}
		return fmt.Sprintf("%s: failed: %s", who, e.Detail)
	case KindNamed:
		return fmt.Sprintf("%s: named %q from its own prompt", who, e.Name)
	case KindPlatform:
		if e.Detail != "" {
			return fmt.Sprintf("%s: %s", who, e.Detail)
		}
		return fmt.Sprintf("%s: %s", who, e.Platform)
	}
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s: %s", who, e.Kind, e.Detail)
	}
	return fmt.Sprintf("%s: %s", who, e.Kind)
}

// Emit receives events. The nil Emit is a working no-op, so the engine has no
// nil checks and a CLI that wants no event stream simply does not set one.
type Emit func(Event)

// Send delivers an event, filling At when the caller did not.
func (e Emit) Send(ev Event) {
	if e == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	e(ev)
}
