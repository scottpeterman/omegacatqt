// internal/capturerun/run.go
//
// The run model: events in, table rows out.
//
// One row per (device, capture type) pair, which is the decision that shapes
// this whole file. A device row would have had to reduce three capture
// outcomes to one status, and the reduction that reads best — worst wins — is
// exactly the one that hides the useful case: a device whose config came back
// fine and whose tech-support timed out is not a failed device, and calling it
// one sends someone to look at the wrong thing.
//
// Device-level facts (platform, the reason a device was never reached) are
// held once and copied onto that device's rows, so sorting by platform works
// without every event having to carry one.
package capturerun

import (
	"sort"
	"sync"
	"time"
)

// State is where a (device, type) pair ended up.
type State int

const (
	StateRunning State = iota
	// StateStored means new content was written.
	StateStored
	// StateUnchanged means the device answered and the content matched the
	// previous capture. A success, and the common one on a schedule.
	StateUnchanged
	// StateNotApplicable means this platform has no command for this type.
	// The third outcome, and the reason there are three: without it every
	// Junos box reads as a startup-config failure forever.
	StateNotApplicable
	// StateFailed means the command, the session, or the store failed.
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateStored:
		return "stored"
	case StateUnchanged:
		return "unchanged"
	case StateNotApplicable:
		return "not applicable"
	case StateFailed:
		return "failed"
	}
	return "running"
}

// OK reports whether a state means the capture did what it should. Unchanged
// and not-applicable both count: nothing is wrong in either case, and a run
// summary that treats them as anything else is wrong more often than it is
// right.
func (s State) OK() bool {
	return s == StateStored || s == StateUnchanged || s == StateNotApplicable
}

// Row is one (device, capture type) pair.
type Row struct {
	Identity string
	Type     string

	// Name is the canonical device name once resolved.
	Name string
	// Platform is the fingerprint, copied from the device record.
	Platform string

	State   State
	Command string
	Bytes   int
	SHA     string

	// Path is the stored file on disk, once there is one. On an unchanged
	// capture it names the EXISTING file whose content matched, which is
	// the right answer for a view: the operator asked to see this device's
	// running-config, and "nothing was written this time" is not it.
	//
	// Empty for a pair that failed or does not apply, and that emptiness is
	// the view's own test for whether a row can be opened.
	Path string

	// Detail is the one out-of-the-ordinary fact about this pair: a
	// failure reason, why it does not apply, how the device got named.
	Detail string

	Started  time.Time
	Finished time.Time

	// Parse outcome for the stored file; see Event. Empty ParseStatus means
	// this type is not parsed.
	ParseStatus  string
	Template     string
	ParseScore   float64
	ParseRecords int

	// Seq is the sequence number of this row's last change. RowsSince
	// compares against it.
	Seq uint64
}

func (r *Row) setParse(ev Event) {
	r.ParseStatus, r.Template = ev.ParseStatus, ev.Template
	r.ParseScore, r.ParseRecords = ev.ParseScore, ev.ParseRecords
}

// Display is the name to show for this row's device: the canonical name once
// the store or the prompt has supplied one, and the run identity until then.
func (r Row) Display() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Identity
}

// Duration is how long the capture took, or how long it has been running.
func (r Row) Duration() time.Duration {
	if r.Started.IsZero() {
		return 0
	}
	if r.Finished.IsZero() {
		return time.Since(r.Started)
	}
	return r.Finished.Sub(r.Started)
}

// device holds the facts that belong to a box rather than to a capture.
type device struct {
	identity string
	name     string
	platform string
	failed   bool
	failWhy  string
}

// Counts summarizes a run. Pairs, not devices — the run is a set of captures
// and counting boxes would answer a question nobody asked.
type Counts struct {
	Devices        int
	DevicesFailed  int
	Stored         int
	Unchanged      int
	NotApplicable  int
	Failed         int
	Running        int
	BytesStored    int
	NewHostKeys    int
	CredRejections int
}

// Run accumulates events into rows. Safe for concurrent Emit from the
// engine's workers.
type Run struct {
	mu       sync.Mutex
	rows     map[Pair]*Row
	order    []Pair
	devices  map[string]*device
	devOrder []string
	notable  []Event
	counts   Counts
	started  time.Time
	finished time.Time

	// changed fires after every applied event. Installed through OnChange
	// so the write is under the same lock as the read; a bare field is
	// written by whoever constructs the view while the engine's workers are
	// already emitting, which is a race the detector only sometimes sees.
	changed func()

	// seq numbers every change: each applied event, and each row Finish
	// settles without one. A row carries the seq of its last change, so a
	// view pulls only what moved (RowsSince) instead of the whole table on
	// every wake.
	seq uint64
}

// New returns an empty run.
func New() *Run {
	return &Run{
		rows:    map[Pair]*Row{},
		devices: map[string]*device{},
		started: time.Now(),
	}
}

// OnChange installs a redraw hook. It is called from the engine's worker
// goroutines and outside the run's lock, so the callback may read the run —
// and a toolkit view must marshal to its own thread inside it.
//
// One hook, last writer wins. A run has one view; a fan-out here would be a
// second way to observe a run alongside the event stream that already exists
// for exactly that.
func (r *Run) OnChange(f func()) {
	r.mu.Lock()
	r.changed = f
	r.mu.Unlock()
}

// Emit returns the sink to hand the engine.
func (r *Run) Emit() Emit { return r.apply }

func (r *Run) apply(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	r.mu.Lock()
	r.seq++
	ev.Seq = r.seq
	r.applyLocked(ev)
	changed := r.changed
	r.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// alreadySaid reports whether an event would only repeat something the
// decisions list already carries.
//
// A device that cannot be dialed emits one device-level failure and then one
// capture failure per selected type, all with the same reason — the per-type
// events exist so the table has a row for every pair, which is how an
// unreachable device stays distinguishable from one nobody asked about. In the
// decisions list they are pure repetition: thirteen dead devices with four
// capture types selected is fifty-two identical lines, and a list nobody can
// read is a list nobody reads.
//
// The rows still get their reason. Only the summary is deduplicated, and only
// when the device itself has already been reported — a capture failing on a
// device that is otherwise healthy is exactly the thing this list is for.
func (r *Run) alreadySaid(ev Event) bool {
	if ev.Kind != KindCaptureFail || ev.Identity == "" {
		return false
	}
	d, ok := r.devices[ev.Identity]
	return ok && d.failed
}

func (r *Run) applyLocked(ev Event) {
	if ev.Notable() && !r.alreadySaid(ev) {
		r.notable = append(r.notable, ev)
	}
	// Host-key and credential facts belong to the run, not to a device. They
	// are counted before the identity guard because the callbacks they come
	// from are handed a host string rather than a device — a dialer that
	// reports first contact without one would otherwise put an event in the
	// decisions list that no counter agrees with, and "three decisions, zero
	// new host keys" is a discrepancy someone will spend an afternoon on.
	// Counting them here also keeps them out of Counts.Devices, which is
	// meant to be the devices asked for.
	switch ev.Kind {
	case KindHostKeyNew:
		r.counts.NewHostKeys++
		return
	case KindAuthReject:
		r.counts.CredRejections++
		return
	}

	if ev.Identity == "" {
		return
	}
	d := r.device(ev.Identity)

	switch ev.Kind {
	case KindQueued:
		return
	case KindPlatform:
		d.platform = ev.Platform
		r.stampPlatform(d)
		return
	case KindResolved, KindNamed:
		if ev.Name != "" {
			d.name = ev.Name
			r.stampName(d)
		}
		return
	case KindDeviceFail:
		if !d.failed {
			d.failed = true
			d.failWhy = reason(ev)
			r.counts.DevicesFailed++
		}
		// Any pair already opened for this device settles as failed —
		// a device that died mid-run must not leave rows reading
		// "running" forever, which is the bug crawlrun's Finish had to
		// sweep up after.
		for _, p := range r.order {
			if p.Identity != ev.Identity {
				continue
			}
			if row := r.rows[p]; row.State == StateRunning {
				r.settle(row, StateFailed, d.failWhy)
			}
		}
		return
	}

	if ev.Type == "" {
		return
	}
	row := r.row(ev)

	switch ev.Kind {
	case KindCaptureStart:
		row.Command = ev.Command
		row.Started = ev.At
		row.Seq = r.seq
	case KindStored:
		// Gated on the settle rather than done alongside it. The state
		// and the counters are protected against a terminal event
		// arriving twice; the byte total has to be too, or a duplicate
		// leaves Stored reading 1 and BytesStored reading double, and
		// the number that is wrong is the one nobody can check by eye.
		if r.settle(row, StateStored, ev.Detail) {
			row.Bytes, row.SHA, row.Path = ev.Bytes, ev.SHA, ev.Path
			row.setParse(ev)
			r.counts.BytesStored += ev.Bytes
		}
	case KindUnchanged:
		if r.settle(row, StateUnchanged, ev.Detail) {
			row.Bytes, row.SHA, row.Path = ev.Bytes, ev.SHA, ev.Path
			row.setParse(ev)
		}
	case KindNotApplic:
		r.settle(row, StateNotApplicable, applicDetail(ev))
	case KindCaptureFail:
		r.settle(row, StateFailed, reason(ev))
	}
}

func applicDetail(ev Event) string {
	if ev.Detail != "" {
		return ev.Detail
	}
	if ev.Platform != "" {
		return "no command for " + ev.Platform
	}
	return "no command for this platform"
}

func reason(ev Event) string {
	if ev.Err != nil {
		return ev.Err.Error()
	}
	if ev.Detail != "" {
		return ev.Detail
	}
	return "failed with no reason given"
}

// settle moves a row out of running exactly once, reporting whether it did.
//
// Counting on the transition rather than recomputing from the map keeps the
// counters honest when an engine emits a terminal event twice, which is a bug
// worth surviving rather than double-counting. The return value exists so that
// everything else a terminal event carries — bytes, digest — is accounted on
// the same condition rather than on its own.
func (r *Run) settle(row *Row, s State, detail string) bool {
	if row.State != StateRunning {
		return false
	}
	row.State = s
	if detail != "" {
		row.Detail = detail
	}
	row.Finished = time.Now()
	row.Seq = r.seq
	r.counts.Running--
	switch s {
	case StateStored:
		r.counts.Stored++
	case StateUnchanged:
		r.counts.Unchanged++
	case StateNotApplicable:
		r.counts.NotApplicable++
	case StateFailed:
		r.counts.Failed++
	}
	return true
}

func (r *Run) device(identity string) *device {
	d, ok := r.devices[identity]
	if !ok {
		d = &device{identity: identity}
		r.devices[identity] = d
		r.devOrder = append(r.devOrder, identity)
		r.counts.Devices++
	}
	return d
}

func (r *Run) row(ev Event) *Row {
	p := ev.Pair()
	row, ok := r.rows[p]
	if !ok {
		d := r.device(ev.Identity)
		row = &Row{
			Identity: p.Identity,
			Type:     p.Type,
			Name:     d.name,
			Platform: d.platform,
			State:    StateRunning,
			Started:  ev.At,
			Seq:      r.seq,
		}
		r.rows[p] = row
		r.order = append(r.order, p)
		r.counts.Running++
	}
	return row
}

func (r *Run) stampPlatform(d *device) {
	for _, p := range r.order {
		if p.Identity == d.identity {
			r.rows[p].Platform = d.platform
			r.rows[p].Seq = r.seq
		}
	}
}

func (r *Run) stampName(d *device) {
	for _, p := range r.order {
		if p.Identity == d.identity {
			r.rows[p].Name = d.name
			r.rows[p].Seq = r.seq
		}
	}
}

// Finish closes the run, settling anything still in flight.
//
// A row left running at the end is a bug in the engine's emit sites, not a
// timeout — so it settles with a reason that says exactly that instead of
// something plausible. crawlrun learned this the hard way: "run ended before
// this device completed" was read as a timeout for a whole session before it
// turned out to be a missing emit.
func (r *Run) Finish() {
	r.mu.Lock()
	r.finished = time.Now()
	for _, p := range r.order {
		if row := r.rows[p]; row.State == StateRunning {
			// A row Finish settles changes with no event behind it, so it
			// gets its own seq; a view reading RowsSince would otherwise
			// never see it end.
			r.seq++
			r.settle(row, StateFailed,
				"run ended with no result for this capture (missing emit, not a timeout)")
		}
	}
	changed := r.changed
	r.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// Rows returns a snapshot in event order.
func (r *Run) Rows() []Row {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Row, 0, len(r.order))
	for _, p := range r.order {
		out = append(out, *r.rows[p])
	}
	return out
}

// RowsSorted returns a snapshot sorted by device then capture type, which is
// the reading order: everything about one box together.
func (r *Run) RowsSorted() []Row {
	out := r.Rows()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Identity != out[j].Identity {
			return out[i].Identity < out[j].Identity
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// RowsByState returns only the rows in one state, for a counter that filters
// the table when it is clicked.
func (r *Run) RowsByState(s State) []Row {
	out := make([]Row, 0, 8)
	for _, row := range r.Rows() {
		if row.State == s {
			out = append(out, row)
		}
	}
	return out
}

// Sorted returns rows ordered by a named column, for table header clicks.
//
// Every ordering falls back to (device, type) for ties rather than leaving
// them in arrival order. A run sorted by state would otherwise reshuffle the
// rows inside each state on every redraw, because arrival order is whatever
// the workers happened to finish in — a table that moves under the cursor
// while it is being read is worse than one that is sorted wrong.
func (r *Run) Sorted(column string, asc bool) []Row {
	rows := r.Rows()
	pair := func(i, j int) bool {
		if rows[i].Identity != rows[j].Identity {
			return rows[i].Identity < rows[j].Identity
		}
		return rows[i].Type < rows[j].Type
	}
	var key func(i, j int) (bool, bool)
	switch column {
	case "type":
		key = func(i, j int) (bool, bool) { return rows[i].Type < rows[j].Type, rows[i].Type == rows[j].Type }
	case "platform":
		key = func(i, j int) (bool, bool) {
			return rows[i].Platform < rows[j].Platform, rows[i].Platform == rows[j].Platform
		}
	case "state":
		key = func(i, j int) (bool, bool) { return rows[i].State < rows[j].State, rows[i].State == rows[j].State }
	case "bytes":
		key = func(i, j int) (bool, bool) { return rows[i].Bytes < rows[j].Bytes, rows[i].Bytes == rows[j].Bytes }
	case "duration":
		key = func(i, j int) (bool, bool) {
			a, b := rows[i].Duration(), rows[j].Duration()
			return a < b, a == b
		}
	case "detail":
		key = func(i, j int) (bool, bool) { return rows[i].Detail < rows[j].Detail, rows[i].Detail == rows[j].Detail }
	default: // "device", and anything unrecognized
		key = func(i, j int) (bool, bool) {
			return rows[i].Display() < rows[j].Display(), rows[i].Display() == rows[j].Display()
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		a, b := i, j
		if !asc {
			a, b = j, i
		}
		less, equal := key(a, b)
		if equal {
			// Ties break on the natural order in BOTH directions, so
			// reversing the sort does not reverse the tiebreak and
			// scatter one device's captures.
			return pair(i, j)
		}
		return less
	})
	return rows
}

// Counts returns a snapshot of the summary.
func (r *Run) Counts() Counts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts
}

// Decisions returns only the events worth reading.
//
// Named to match crawlrun.Run.Decisions. The two run models are separate
// types on purpose, but a view written against one and then adapted to the
// other should not have to discover that the same list is called something
// else here.
func (r *Run) Decisions() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.notable...)
}

// Elapsed is the run's wall time.
func (r *Run) Elapsed() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished.IsZero() {
		return time.Since(r.started)
	}
	return r.finished.Sub(r.started)
}

// Seq is the sequence number of the most recent change.
func (r *Run) Seq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// RowsSince returns the rows changed after seq, in the order they first
// appeared, and the sequence number to pass next time.
func (r *Run) RowsSince(seq uint64) ([]Row, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Row
	for _, p := range r.order {
		if row := r.rows[p]; row.Seq > seq {
			out = append(out, *row)
		}
	}
	return out, r.seq
}

// DecisionsSince returns the decisions recorded after seq, oldest first, and
// the sequence number to pass next time. The list is never trimmed, so a view
// that pulls late still sees every decision.
func (r *Run) DecisionsSince(seq uint64) ([]Event, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, ev := range r.notable {
		if ev.Seq > seq {
			out = append(out, ev)
		}
	}
	return out, r.seq
}

// Progress is what a progress view needs, in one consistent read.
//
// Rows appear as devices are visited, so Total grows while a run is under
// way; Counts.Devices is how many devices have been seen so far. Settled over
// Total is the honest fraction of pairs done at this moment, not a forecast.
type Progress struct {
	Seq      uint64
	Elapsed  time.Duration
	Finished bool
	Counts   Counts

	// Total is every (device, type) row so far; Settled is how many of them
	// are out of Running.
	Total   int
	Settled int

	// Running is the pairs in flight, longest-running first: the head of
	// the list is what the run is waiting on.
	Running []Row
}

// Progress returns the run's progress.
func (r *Run) Progress() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := Progress{
		Seq:      r.seq,
		Counts:   r.counts,
		Finished: !r.finished.IsZero(),
		Total:    len(r.order),
	}
	if p.Finished {
		p.Elapsed = r.finished.Sub(r.started)
	} else {
		p.Elapsed = time.Since(r.started)
	}
	for _, pair := range r.order {
		row := r.rows[pair]
		if row.State == StateRunning {
			p.Running = append(p.Running, *row)
		} else {
			p.Settled++
		}
	}
	sort.SliceStable(p.Running, func(i, j int) bool {
		return p.Running[i].Started.Before(p.Running[j].Started)
	})
	return p
}
