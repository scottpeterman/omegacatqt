package storesearch

// internal/storesearch/search.go
//
// Full-text search across a capture store.
//
// # What it searches, and what it deliberately does not
//
// The CURRENT version of every capture, and nothing else. The store keeps a
// file per changed capture and a history line per attempt, so a device backed
// up nightly for a year holds one file per real change and hundreds of history
// entries pointing at those same files. Searching history means reading
// identical bytes once per night that nothing changed, and returning the same
// line as hundreds of separate hits. Searching versions is a real feature —
// "when did this appear" — and it is a different feature with a different
// result shape, not a flag on this one.
//
// # Why it does not have an event stream
//
// A crawl and a capture each have a run model and an event stream because they
// are long, remote, and failure-rich: a device can be unreachable, refuse a
// credential, or answer slowly, and the operator needs to see which. A search
// reads local files. It runs to completion, it either found something or it
// did not, and the only progress worth showing is a count. So this returns a
// Result rather than emitting one, and the caller runs it on a goroutine.
//
// # The bound that is not really a bound
//
// MaxFileBytes skips an artifact that is too large to scan, but capture.Browser
// has no streaming read — Read hands back the whole file. So the ceiling
// bounds MATCHING work and memory held across the scan; it does not stop the
// file being read into memory once. Saying so here because the field name
// promises more than the interface can deliver, and the honest fix lives in
// Browser rather than in this package.
import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/scottpeterman/omegacatqt/internal/capture"
)

// Defaults. Every one of these is a guard rather than a tuning knob, and the
// numbers are chosen to be invisible on a healthy store and to bite before a
// pathological one takes the process with it.
const (
	// DefaultLimit caps the hits held in memory. A one-character query
	// over a large store otherwise builds a table model with a row per
	// matching line of every config in the estate.
	DefaultLimit = 5000

	// DefaultConcurrency reads this many artifacts at once. These are
	// local file reads, so the useful number is bounded by the disk rather
	// than by the estate.
	DefaultConcurrency = 8

	// DefaultMaxFileBytes skips an artifact larger than this. It is the
	// capture side's config bound, not a number of its own: a store that
	// accepts a config search will not read is a search that answers
	// "not configured anywhere" for the one device that has it.
	DefaultMaxFileBytes = capture.ConfigMaxBytes

	// DefaultTrimTo is how much of a matching line survives into a Hit.
	// Configuration lines are long and a table column is not; the full
	// line is one read away in the content pane.
	DefaultTrimTo = 300
)

// Hit is one matching line.
type Hit struct {
	// Device is the canonical device name — the same key the store and
	// the capture views use, never a slug and never a path.
	Device string
	Type   string
	// File names the artifact this line came from, so a viewer can open
	// exactly what was searched rather than re-resolving "the current one"
	// and possibly getting a newer file.
	File string

	// Line is 1-based, matching what every editor and every device's own
	// error messages call a line number.
	Line int
	// Text is the matching line, trimmed and truncated for display.
	Text string
	// Truncated says Text is shorter than the line was.
	Truncated bool
	// Indent is how many leading whitespace bytes were trimmed. It is the
	// cheapest answer to "was this inside a block or at top level", which
	// trimming otherwise destroys.
	Indent int
}

// SkipReason says why an artifact was not searched.
type SkipReason string

const (
	SkipTooLarge SkipReason = "too large"
	SkipNotText  SkipReason = "not text"
	SkipUnread   SkipReason = "unreadable"
	SkipNoTypes  SkipReason = "types unreadable"
)

// Skip records an artifact that was not searched. Skips are returned rather
// than logged: a search that quietly declines to look at a device reports the
// same empty result as a search that looked and found nothing, and those two
// answers are not the same answer.
type Skip struct {
	Device string
	Type   string
	File   string
	Reason SkipReason
	Err    string
}

func (s Skip) String() string {
	where := s.Device
	if s.Type != "" {
		where += " / " + s.Type
	}
	if s.Err != "" {
		return fmt.Sprintf("%s: %s (%s)", where, s.Reason, s.Err)
	}
	return fmt.Sprintf("%s: %s", where, s.Reason)
}

// Options configures a search. The zero value is usable — every field falls
// back to its Default above.
type Options struct {
	// Types restricts the search to these capture types. Empty means
	// every type the store holds.
	Types []string

	Limit        int
	Concurrency  int
	MaxFileBytes int64
	TrimTo       int

	// OnProgress is called as artifacts complete, from scan workers, so it
	// must be safe for concurrent use and must not block. It is a counter
	// for a status line, not an event stream: done counts artifacts
	// finished, total is how many there are.
	OnProgress func(done, total int)
}

func (o Options) limit() int {
	if o.Limit > 0 {
		return o.Limit
	}
	return DefaultLimit
}

func (o Options) concurrency() int {
	if o.Concurrency > 0 {
		return o.Concurrency
	}
	return DefaultConcurrency
}

func (o Options) maxFileBytes() int64 {
	if o.MaxFileBytes > 0 {
		return o.MaxFileBytes
	}
	return DefaultMaxFileBytes
}

func (o Options) trimTo() int {
	if o.TrimTo > 0 {
		return o.TrimTo
	}
	return DefaultTrimTo
}

// Result is everything one search learned.
type Result struct {
	// Hits are sorted by device, then type, then line — deterministically,
	// so the same query over an unchanged store renders identically twice.
	// Scan order is concurrent and therefore is not that order; see the
	// note on Search.
	Hits []Hit

	// Capped says the limit was reached and Hits is not every match. It
	// exists so the view can say so: a truncated list that does not admit
	// it is a wrong answer rather than a partial one.
	Capped bool
	Limit  int

	// Devices and Artifacts are what was actually looked at, which is the
	// number that makes an empty result interpretable — no hits across
	// four hundred artifacts means something different from no hits across
	// none.
	Devices   int
	Artifacts int
	Bytes     int64

	// Skips are artifacts that could not be searched.
	Skips []Skip
	// Warning carries a store-level problem that did not stop the search,
	// notably capture.UnreadableDevices, which returns a usable list
	// alongside its error.
	Warning error

	Elapsed time.Duration
}

// Summary is the one line a status bar wants.
func (r Result) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d hit%s", len(r.Hits), plural(len(r.Hits)))
	if r.Capped {
		fmt.Fprintf(&b, " (stopped at %d)", r.Limit)
	}
	fmt.Fprintf(&b, " · %d artifact%s across %d device%s",
		r.Artifacts, plural(r.Artifacts), r.Devices, plural(r.Devices))
	if n := len(r.Skips); n > 0 {
		fmt.Fprintf(&b, " · %d skipped", n)
	}
	if r.Elapsed > 0 {
		fmt.Fprintf(&b, " · %s", r.Elapsed.Round(time.Millisecond))
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Target is one artifact to scan: the current file of one (device, type).
type Target struct {
	Device string
	Type   string
	File   string
}

// TargetSet is what a search is about to read, plus everything learned while
// working that out.
//
// Warning is a store-level problem that left a usable list — notably
// capture.UnreadableDevices, which names device directories that exist but
// could not be identified and returns the readable ones alongside. That is a
// warning rather than a failure because a search showing nothing on account of
// one damaged directory is worse than one showing the rest and saying so.
type TargetSet struct {
	Targets []Target
	Devices int
	Skips   []Skip
	Warning error
}

// Targets resolves what a search would read, without reading any of it.
//
// It is exported and separate from Search for two reasons. A caller can show
// "searching 412 artifacts" before the first byte is read; and this is the
// expensive half on a large store — capture.Browser.Types parses that device's
// whole history.jsonl to answer, which is per-device bounded and documented as
// accepted debt, but a search is the first consumer that pays it for every
// device at once. When that becomes the slow part, a cached or summarised
// target list drops in here and Search does not change.
func Targets(b capture.Browser, types []string) (TargetSet, error) {
	devices, warn := b.Devices()
	if len(devices) == 0 && warn != nil {
		// No usable list at all. Reporting this as a warning and then
		// returning "no matches" is the same wrong answer in a
		// friendlier voice.
		return TargetSet{}, warn
	}

	want := map[string]bool{}
	for _, t := range types {
		if t = strings.TrimSpace(t); t != "" {
			want[t] = true
		}
	}

	var (
		out   []Target
		skips []Skip
	)
	for _, d := range devices {
		tis, err := b.Types(d.Canonical)
		if err != nil {
			skips = append(skips, Skip{
				Device: d.Canonical, Reason: SkipNoTypes, Err: err.Error(),
			})
			continue
		}
		for _, ti := range tis {
			if len(want) > 0 && !want[ti.Type] {
				continue
			}
			// A type with attempts but no stored file has never
			// produced content — it is not a skip, there is
			// simply nothing to read.
			if ti.File == "" {
				continue
			}
			out = append(out, Target{Device: d.Canonical, Type: ti.Type, File: ti.File})
		}
	}

	// Sorting here rather than sorting hits at the end is what makes the
	// capped case honest: workers take targets in this order, so a capped
	// result is the front of the answer rather than an arbitrary sample of
	// it.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].Type < out[j].Type
	})
	return TargetSet{Targets: out, Devices: len(devices), Skips: skips, Warning: warn}, nil
}

// Search scans the current version of every capture in the store.
//
// Order: hits come back sorted by device, then type, then line. The scan
// itself is concurrent and completes in whatever order the disk allows, so
// results are collected per artifact and concatenated in target order at the
// end. Without that a second run of the same query over an unchanged store
// renders in a different order, and a list that reshuffles is a list nobody
// trusts.
//
// Cancellation is checked between artifacts and before each read. An
// individual read is not interruptible because Browser.Read is not, so worst
// case latency on cancel is one artifact.
func Search(ctx context.Context, b capture.Browser, m Matcher, opts Options) (Result, error) {
	started := time.Now()
	if b == nil {
		return Result{}, fmt.Errorf("storesearch: no store")
	}
	if m == nil {
		return Result{}, fmt.Errorf("storesearch: no matcher")
	}

	ts, err := Targets(b, opts.Types)
	if err != nil {
		return Result{}, err
	}
	targets := ts.Targets
	res := Result{
		Limit:   opts.limit(),
		Devices: ts.Devices,
		Skips:   ts.Skips,
		Warning: ts.Warning,
	}
	if len(targets) == 0 {
		res.Elapsed = time.Since(started)
		return res, ctx.Err()
	}

	var (
		mu      sync.Mutex
		perT    = make([][]Hit, len(targets))
		found   atomic.Int64
		scanned atomic.Int64
		bytesIn atomic.Int64
		done    atomic.Int64
	)

	limit := opts.limit()
	maxBytes := opts.maxFileBytes()
	trimTo := opts.trimTo()

	work := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < opts.concurrency(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range work {
				t := targets[idx]
				if ctx.Err() != nil {
					return
				}
				// Once the cap is reached the remaining
				// targets are drained without being read.
				// Draining rather than closing keeps the
				// dispatch loop simple and costs nothing.
				if found.Load() >= int64(limit) {
					continue
				}

				data, err := b.Read(t.Device, t.Type, t.File)
				if err != nil {
					mu.Lock()
					res.Skips = append(res.Skips, Skip{
						Device: t.Device, Type: t.Type, File: t.File,
						Reason: SkipUnread, Err: err.Error(),
					})
					mu.Unlock()
					done.Add(1)
					progress(opts, &done, len(targets))
					continue
				}
				if int64(len(data)) > maxBytes {
					mu.Lock()
					res.Skips = append(res.Skips, Skip{
						Device: t.Device, Type: t.Type, File: t.File,
						Reason: SkipTooLarge,
						Err:    fmt.Sprintf("%d bytes", len(data)),
					})
					mu.Unlock()
					done.Add(1)
					progress(opts, &done, len(targets))
					continue
				}
				if !utf8.Valid(data) {
					mu.Lock()
					res.Skips = append(res.Skips, Skip{
						Device: t.Device, Type: t.Type, File: t.File,
						Reason: SkipNotText,
					})
					mu.Unlock()
					done.Add(1)
					progress(opts, &done, len(targets))
					continue
				}

				hits := scan(data, m, t, trimTo)
				scanned.Add(1)
				bytesIn.Add(int64(len(data)))
				if len(hits) > 0 {
					// Claim room under the cap before
					// keeping anything, so the total
					// held is bounded even when several
					// artifacts finish at once.
					room := int64(limit) - found.Add(int64(len(hits)))
					if room < 0 {
						keep := int64(len(hits)) + room
						if keep < 0 {
							keep = 0
						}
						hits = hits[:keep]
					}
					perT[idx] = hits
				}
				done.Add(1)
				progress(opts, &done, len(targets))
			}
		}()
	}

dispatch:
	for i := range targets {
		select {
		case <-ctx.Done():
			break dispatch
		case work <- i:
		}
	}
	close(work)
	wg.Wait()

	for _, hs := range perT {
		res.Hits = append(res.Hits, hs...)
	}
	res.Capped = found.Load() >= int64(limit)
	res.Artifacts = int(scanned.Load())
	res.Bytes = bytesIn.Load()
	res.Elapsed = time.Since(started)

	// Skips arrive in completion order from every worker; sorting them
	// makes two runs of one query comparable, same reason as the hits.
	sort.Slice(res.Skips, func(i, j int) bool {
		if res.Skips[i].Device != res.Skips[j].Device {
			return res.Skips[i].Device < res.Skips[j].Device
		}
		return res.Skips[i].Type < res.Skips[j].Type
	})
	return res, ctx.Err()
}

func progress(o Options, done *atomic.Int64, total int) {
	if o.OnProgress != nil {
		o.OnProgress(int(done.Load()), total)
	}
}

// scan walks one artifact.
//
// Lines are split by hand rather than with bufio.Scanner, which caps a token
// at 64KB by default and would report a long line as an error rather than as
// a line. A captured artifact is whatever the device said, and being unable to
// search a file because one line is long is the wrong failure.
func scan(data []byte, m Matcher, t Target, trimTo int) []Hit {
	var hits []Hit
	line := 0
	for len(data) > 0 {
		line++
		var raw []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			raw, data = data[:i], data[i+1:]
		} else {
			raw, data = data, nil
		}
		raw = bytes.TrimSuffix(raw, []byte("\r"))

		s := string(raw)
		if !m.MatchLine(s) {
			continue
		}
		text, indent, truncated := display(s, trimTo)
		hits = append(hits, Hit{
			Device: t.Device, Type: t.Type, File: t.File,
			Line: line, Text: text, Indent: indent, Truncated: truncated,
		})
	}
	return hits
}

// display trims a line for a table cell and reports what it removed.
func display(s string, trimTo int) (text string, indent int, truncated bool) {
	left := strings.TrimLeft(s, " \t")
	indent = len(s) - len(left)
	text = strings.TrimRight(left, " \t")
	if trimTo > 0 && len(text) > trimTo {
		// Cut on a rune boundary — a table cell rendering a broken
		// UTF-8 sequence looks like a corrupted capture rather than
		// like a truncated one.
		cut := trimTo
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
		truncated = true
	}
	return text, indent, truncated
}
