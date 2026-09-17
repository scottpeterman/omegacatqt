// internal/capture/capture.go
//
// The capture engine: connect, identify, read, store.
//
// Nothing here writes to a device. Every command comes from a Spec, every Spec
// command is on the read-only allowlist enforced in spec_test.go, and the
// engine has no path that sends a string it was handed at runtime.
//
// # Shape
//
// One visit per device. A device is dialed once, fingerprinted once, and then
// every selected capture type runs over that one session — cheap commands
// first, so a wedge on an expensive one does not cost the artifacts that were
// already available. Per-command bounds come from the spec via
// netexec.RunWith, which is why one session can carry both a config and a
// tech-support without either sizing the other.
//
// # Identity
//
// Storage keys on the canonical name from the binding store, resolved before
// anything is written. When the store has never heard of the device, the
// device's own prompt supplies the name and the engine binds it — capture is a
// contributor to identity, not just a reader. That matters because the common
// case here is a device list rather than a crawl result, so a device no fold
// has ever touched is ordinary rather than exceptional, and a read-only engine
// would file every one of them under whatever string the list happened to use.
//
// Only first-hand evidence is bound: the prompt of a device actually
// authenticated to, and the address it answered on. Never a name from a list,
// which is a claim rather than an observation.
package capture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/credres"
	"github.com/scottpeterman/omegacatqt/internal/dial"
	"github.com/scottpeterman/omegacatqt/internal/netexec"
	"github.com/scottpeterman/omegacatqt/internal/normalize"
	"github.com/scottpeterman/omegacatqt/internal/sshcore"
)

// Device is one entry in the capture list.
type Device struct {
	// Target is the string to dial.
	Target string

	// Identity is the key this device is tracked under for the duration of
	// the run. Defaults to Target. It is NOT the storage key — that is the
	// canonical name, resolved after connecting.
	Identity string

	// Addr is a known address, when the list carries one.
	Addr string

	// Aliases are other shapes this device is known by, widening the
	// binding lookup. Claims, not evidence: they are never bound.
	Aliases []string

	// Credential names a vault entry this device is known to use, by name
	// or id. It is tried first and the ladder still follows it.
	//
	// Set when the device came from somewhere that already records the
	// answer — a session in the inventory names its credential, and a
	// capture driven from that inventory should not have to rediscover it
	// one failed handshake at a time. Empty for a device list, which
	// carries names and nothing else.
	Credential string

	// Legacy enables old key exchange and ciphers for this device, on top
	// of the run's setting.
	Legacy bool

	// Platform, when set, is the platform to use instead of classifying the
	// device. A person set it, so it wins; the device is still sent that
	// platform's paging command, and a disagreement is reported, not acted
	// on. It must be one of netexec.Platforms.
	Platform string

	// PlatformHint is somebody's guess -- a crawl's -- and only ever
	// compared: when the device classifies as something else, or as
	// nothing, the run says so. It never picks a command.
	PlatformHint string
}

func (d Device) identity() string {
	if d.Identity != "" {
		return d.Identity
	}
	return d.Target
}

// Resolver is the part of the binding store capture needs. An interface rather
// than *credres.FileBindings so the engine is testable without a vault, and so
// a caller that does not want capture writing to its store can pass something
// that drops the writes.
type Resolver interface {
	Resolve(ids ...string) (credres.Binding, bool)
	Bind(canonical string, aliases ...string) error
}

// Config configures an Engine.
type Config struct {
	// Dial opens connections. Required.
	Dial dial.Func

	// Store receives artifacts. Required.
	Store Store

	// Specs are the capture types to run, in the order given. Cheap
	// commands are reordered ahead of expensive ones within each device.
	Specs []Spec

	// Bindings resolves and records canonical names. Optional: without it
	// the engine falls back to the device's prompt, then to its identity.
	Bindings Resolver

	// Concurrency is how many devices are visited at once. Default 5.
	Concurrency int

	// ExpensiveConcurrency bounds commands marked CostExpensive across the
	// whole run. Default 1. This is the lane that keeps a fleet-wide
	// tech-support from being a denial of service delivered politely.
	ExpensiveConcurrency int

	// SessionOpts are the netexec defaults. Per-command overrides come
	// from the spec.
	SessionOpts netexec.Options

	// Log receives progress prose; nil discards.
	Log func(format string, args ...any)

	// Emit receives structured events; nil is a working no-op.
	Emit capturerun.Emit

	// Parser parses the output of commands that carry a TemplateHint, into a
	// .parsed.json beside the stored file. Nil stores without parsing, and so
	// does a Store that is not a ParsedStore.
	Parser Parser
}

// Result is one (device, capture type) outcome.
type Result struct {
	Device   string // canonical name as stored, or the identity if unresolved
	Identity string
	Type     string
	Platform string
	Command  string
	Artifact Artifact
	// NotApplicable means the platform has no command for this type. Err
	// is nil and nothing was stored; this is a success.
	NotApplicable bool
	Err           error

	// Parsed is the parse written beside the stored file, when there is one.
	Parsed *ParsedFile
}

// OK reports whether this result needs anyone's attention.
func (r Result) OK() bool { return r.Err == nil }

// Engine captures device state.
type Engine struct {
	cfg      Config
	expLane  chan struct{}
	logf     func(string, ...any)
	specs    []Spec
	warnOnce sync.Once
}

// New builds an engine. Specs are validated up front: a bad spec is a
// programming error and should not be discovered halfway through a fleet.
func New(cfg Config) (*Engine, error) {
	if cfg.Dial == nil {
		return nil, errors.New("capture: Config.Dial is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("capture: Config.Store is required")
	}
	if len(cfg.Specs) == 0 {
		return nil, errors.New("capture: no capture types selected")
	}
	seen := map[string]bool{}
	for _, s := range cfg.Specs {
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("capture: %w", err)
		}
		if seen[s.Type] {
			return nil, fmt.Errorf("capture: %q selected twice", s.Type)
		}
		seen[s.Type] = true
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 5
	}
	if cfg.ExpensiveConcurrency <= 0 {
		cfg.ExpensiveConcurrency = 1
	}
	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Engine{
		cfg:     cfg,
		expLane: make(chan struct{}, cfg.ExpensiveConcurrency),
		logf:    logf,
		specs:   cfg.Specs,
	}, nil
}

// Capture visits every device and returns one Result per (device, spec) pair
// that was attempted or declared not applicable.
//
// A device that could not be reached produces one Result per selected spec,
// each carrying the dial error — not a single device-level error. The unit of
// this run is the pair, and a caller reconciling "what did I ask for against
// what did I get" should not have to special-case the failure shape.
func (e *Engine) Capture(ctx context.Context, devices []Device) []Result {
	sem := make(chan struct{}, e.cfg.Concurrency)
	var (
		mu  sync.Mutex
		out []Result
	)
	var wg sync.WaitGroup

	for _, d := range devices {
		e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindQueued, Identity: d.identity()})
	}

	for _, d := range devices {
		d := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				out = append(out, e.deviceFailed(d, "", ctx.Err())...)
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			// Checked after taking a slot, not before: every worker
			// starts at once and blocks here, so a cancel between
			// queueing and running must fall through rather than
			// dial.
			if err := ctx.Err(); err != nil {
				mu.Lock()
				out = append(out, e.deviceFailed(d, "", err)...)
				mu.Unlock()
				return
			}
			res := e.visit(ctx, d)
			mu.Lock()
			out = append(out, res...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Identity != out[j].Identity {
			return out[i].Identity < out[j].Identity
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// platformNote is what is worth saying about where a device's platform came
// from, or "" when there is nothing to say: detected with no hint, detected
// matching the hint, or set and agreeing with the device.
func platformNote(d Device, fp *netexec.Platform) string {
	if fp.Forced {
		switch {
		case fp.PagingRejected:
			return fmt.Sprintf("platform set to %s, but the device refused %s paging; output may page and the setting is probably wrong",
				fp.Name, fp.Name)
		case fp.Detected == "":
			return fmt.Sprintf("platform set to %s; the device's version output does not look like %s", fp.Name, fp.Name)
		case fp.Detected != fp.Name:
			return fmt.Sprintf("platform set to %s; the device reports %s", fp.Name, fp.Detected)
		}
		return ""
	}
	hint := strings.TrimSpace(d.PlatformHint)
	switch {
	case hint == "" || hint == fp.Name:
		return ""
	case fp.Name == "unknown":
		return fmt.Sprintf("not detected; inventory says %s (set the platform on the device to use it)", hint)
	}
	return fmt.Sprintf("inventory says %s, detected %s", hint, fp.Name)
}

// deviceFailed produces one failed Result per selected spec, and one failed
// EVENT per selected spec alongside the device-level one.
//
// The per-spec events are not redundant. Rows are keyed on (device, type), so
// a device that dies before any capture starts would otherwise contribute no
// rows at all — a device on the list that was never reached would be missing
// from the table entirely, which reads as "not asked for" rather than "asked
// for and unreachable". Those are the two things a capture run exists to tell
// apart.
func (e *Engine) deviceFailed(d Device, platform string, err error) []Result {
	id := d.identity()
	e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindDeviceFail,
		Identity: id, Platform: platform, Err: err})
	e.logf("capture: %s: %v", id, err)
	out := make([]Result, 0, len(e.specs))
	for _, s := range e.specs {
		e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindCaptureFail,
			Identity: id, Type: s.Type, Platform: platform, Err: err})
		out = append(out, Result{
			Device: id, Identity: id, Type: s.Type, Platform: platform, Err: err,
		})
	}
	return out
}

func (e *Engine) visit(ctx context.Context, d Device) []Result {
	id := d.identity()

	client, err := e.cfg.Dial(ctx, dial.Target{
		Target:     d.Target,
		Identity:   id,
		Addr:       d.Addr,
		Reported:   d.Target,
		Credential: d.Credential,
		Legacy:     d.Legacy,
	})
	if err != nil {
		return e.deviceFailed(d, "", fmt.Errorf("dial: %w", err))
	}
	defer client.Close()
	e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindConnected, Identity: id})

	sess, err := netexec.Open(ctx, client, e.cfg.SessionOpts)
	if err != nil {
		return e.deviceFailed(d, "", fmt.Errorf("session: %w", err))
	}
	defer sess.Close()

	var fp *netexec.Platform
	if d.Platform != "" {
		fp, err = netexec.FingerprintAs(ctx, sess, d.Platform)
	} else {
		fp, err = netexec.Fingerprint(ctx, sess)
	}
	if err != nil || fp == nil {
		return e.deviceFailed(d, "", fmt.Errorf("fingerprint: %w", err))
	}
	e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindPlatform,
		Identity: id, Platform: fp.Name, Detail: platformNote(d, fp)})

	info := e.identify(d, sess, client, fp.Name)

	var out []Result
	for _, s := range e.ordered() {
		select {
		case <-ctx.Done():
			out = append(out, Result{Device: info.Canonical, Identity: id,
				Type: s.Type, Platform: fp.Name, Err: ctx.Err()})
			e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindCaptureFail,
				Identity: id, Type: s.Type, Err: ctx.Err()})
			continue
		default:
		}
		out = append(out, e.one(ctx, sess, id, info, fp, s))
	}
	e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindDeviceDone,
		Identity: id, Name: info.Canonical, Platform: fp.Name})
	return out
}

// ordered returns the specs with cheap commands first.
//
// Stable, so a caller's ordering survives within each lane. The point is only
// that a wedged tech-support must not be the reason a config was never
// collected from a device the engine already had a session to.
func (e *Engine) ordered() []Spec {
	out := append([]Spec(nil), e.specs...)
	sort.SliceStable(out, func(i, j int) bool {
		return specCost(out[i]) < specCost(out[j])
	})
	return out
}

// specCost is the highest cost any platform's command in the spec declares.
// Taking the maximum rather than the minimum is deliberate: the ordering
// should be pessimistic, since being wrong the other way puts an expensive
// command first and loses the cheap artifacts it was meant to protect.
func specCost(s Spec) Cost {
	worst := CostCheap
	for _, c := range s.Commands {
		if c.Cost > worst {
			worst = c.Cost
		}
	}
	return worst
}

// identify settles the storage key for a device.
func (e *Engine) identify(d Device, sess *netexec.Session, client *sshcore.Client, platform string) DeviceInfo {
	id := d.identity()
	ids := append([]string{id, d.Target}, d.Aliases...)
	if d.Addr != "" {
		ids = append(ids, d.Addr)
	}

	// The address the device actually answered on. First-hand, so it is
	// safe to bind, and it is what ties a name-reached visit to an
	// address-reached one later.
	//
	// The port stays on unless it is 22. Behind a port-forward -- a lab
	// host, a container runtime -- every device answers from the same
	// address and only the port tells them apart, so binding the bare host
	// would make that address a strong alias shared by all of them and
	// merge their records into one device.
	answered := ""
	if host, port, err := net.SplitHostPort(client.RemoteAddr()); err == nil {
		answered = host
		if port != "22" {
			answered = net.JoinHostPort(host, port)
		}
	}

	info := DeviceInfo{Canonical: id, Platform: platform}

	// What this visit itself called the device. A binding whose label is
	// one of these has not learned a name -- it is repeating ours back.
	//
	// That is the normal state of a device on first contact through a
	// vault: credres records a binding the moment a credential works, and
	// the only name it has at that moment is the one it was dialed by. So
	// the record exists before identify runs, and resolving it as if it
	// were an answer filed every device under its address forever, with the
	// prompt name below never consulted. A label that is NOT one of ours --
	// a crawl's fold, a name an earlier capture learned from the prompt --
	// is evidence, and wins.
	supplied := map[string]bool{}
	for _, s := range append(ids, answered) {
		if s != "" {
			supplied[normalize.Identifier(s)] = true
		}
	}

	var prior credres.Binding
	havePrior := false
	if e.cfg.Bindings != nil {
		if b, ok := e.cfg.Bindings.Resolve(ids...); ok && b.Canonical != "" {
			prior, havePrior = b, true
			if !supplied[normalize.Identifier(b.Canonical)] {
				info.Canonical = b.Canonical
				info.Aliases = b.Aliases
				e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindResolved,
					Identity: id, Name: b.Canonical})
				e.bindAnswered(b.Canonical, answered)
				return info
			}
		}
	}

	// No learned name. The device's own prompt is the best evidence
	// available, and it is first-hand -- the same standard the post-crawl
	// fold applies, and the reason it will not accept a name off a list.
	if sys := normalize.HostnameFromPrompt(sess.Prompt()); sys != "" {
		info.Canonical = sys
		if e.cfg.Bindings != nil {
			aliases := []string{d.Target}
			if answered != "" {
				aliases = append(aliases, answered)
			}
			if err := e.cfg.Bindings.Bind(sys, aliases...); err != nil {
				e.logf("capture: %s: recording identity failed: %v", id, err)
			}
			info.Aliases = aliases
			// The store picks the label, not this call: binding the prompt
			// name into a record that already holds a qualified name the
			// device was dialed by keeps that name, and the storage key has
			// to be whatever every later run will resolve to.
			if b, ok := e.cfg.Bindings.Resolve(sys); ok && b.Canonical != "" {
				info.Canonical = b.Canonical
				info.Aliases = b.Aliases
			}
		}
		kind := capturerun.KindNamed
		if havePrior && prior.Canonical == info.Canonical {
			// The label did not move -- a device dialed by the FQDN its
			// prompt shortens. Nothing new was learned; saying "named"
			// every night would put it in the decisions list every night.
			kind = capturerun.KindResolved
		}
		e.cfg.Emit.Send(capturerun.Event{Kind: kind, Identity: id, Name: info.Canonical})
		return info
	}

	if havePrior {
		// No prompt name either; the record's label is still the best key,
		// and it is at least stable across runs.
		info.Canonical = prior.Canonical
		info.Aliases = prior.Aliases
	}

	// No binding and no prompt name. Storing under the identity is the
	// honest fallback — it is what the caller called this device — and the
	// history will fold into the right place the first time either a crawl
	// or a later capture does get a name.
	e.warnOnce.Do(func() {
		e.logf("capture: %s: no binding and no name from its prompt; storing under %q", id, id)
	})
	return info
}

func (e *Engine) bindAnswered(canonical, answered string) {
	if e.cfg.Bindings == nil || answered == "" || canonical == "" {
		return
	}
	if err := e.cfg.Bindings.Bind(canonical, answered); err != nil {
		e.logf("capture: %s: recording answering address failed: %v", canonical, err)
	}
}

// one runs a single capture type against an open session.
//
// id is the run identity — the key the caller's device list used and the key
// every event in this run is filed under. info.Canonical is the STORAGE key,
// and the two are routinely different: a device dialed by address and resolved
// through the binding store has an identity of "172.16.1.2" and a canonical of
// "lab-r1.lab.example".
//
// Keeping those apart is the whole point. Filing capture events under the
// canonical while device events use the identity is precisely the crawlrun bug
// this package's header describes — one device, two rows, the platform column
// blank because the stamp never reached rows keyed under the other string.
func (e *Engine) one(ctx context.Context, sess *netexec.Session, id string, info DeviceInfo, fp *netexec.Platform, s Spec) Result {
	platform := fp.Name
	res := Result{Device: info.Canonical, Identity: id, Type: s.Type, Platform: platform}

	cmd, ok := s.For(platform)
	if !ok {
		res.NotApplicable = true
		e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindNotApplic,
			Identity: id, Type: s.Type, Platform: platform})
		return res
	}
	// A platform key is a NOS, not a chassis. When the command only
	// applies to part of the platform's hardware, the fingerprint's
	// version output is what tells them apart — and it is already in hand,
	// so this costs no extra round trip.
	//
	// Checked before res.Command is set, so a device declined here reports
	// no command at all — the same shape as a platform with no entry for
	// this type. Result.Command means "what was sent", which is also what
	// separates this outcome from a refusal: a refused capture carries the
	// command, because the device really was asked.
	if cmd.ModelMatch != nil && !cmd.ModelMatch.MatchString(fp.VersionOutput) {
		res.NotApplicable = true
		e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindNotApplic,
			Identity: id, Type: s.Type, Platform: platform})
		return res
	}

	res.Command = cmd.Command

	if cmd.Cost == CostExpensive {
		select {
		case e.expLane <- struct{}{}:
			defer func() { <-e.expLane }()
		case <-ctx.Done():
			res.Err = ctx.Err()
			e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindCaptureFail,
				Identity: id, Type: s.Type, Command: cmd.Command, Err: res.Err})
			return res
		}
	}

	e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindCaptureStart,
		Identity: id, Type: s.Type, Command: cmd.Command, Platform: platform})

	out, err := sess.RunWith(ctx, cmd.Command, netexec.RunOptions{
		MaxBytes: cmd.MaxBytes,
		Timeout:  cmd.Timeout,
	})
	if err != nil {
		res.Err = fmt.Errorf("%s: %w", cmd.Command, err)
		e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindCaptureFail,
			Identity: id, Type: s.Type, Command: cmd.Command, Err: res.Err})
		return res
	}

	// A refused command is not a transport error: the device replied and
	// returned to its prompt, so err is nil and out holds a perfectly valid
	// string that means "no". Storing it would file that refusal as an
	// artifact, dedup it to unchanged on every later run, and render as a
	// healthy row forever. An empty reply is the same problem with nothing
	// to read — the shape a device with no such subsystem produces.
	//
	// Reporting these as not-applicable is deliberately louder than
	// storing them. A type that shows "n/a" against a device that plainly
	// should support it is a visible prompt to look; a stored refusal is
	// not, and it also becomes the baseline the next real capture is
	// diffed against.
	if netexec.LooksLikeRejection(out) || strings.TrimSpace(out) == "" {
		res.NotApplicable = true
		// Said explicitly. Without a detail the row would fall back to
		// "no command for <platform>", which is the model-gate and
		// missing-entry wording -- "we never asked" -- when this device
		// was asked and said no, which is the one worth a second look on
		// a box that should have the table.
		why := "device refused " + cmd.Command
		if !netexec.LooksLikeRejection(out) {
			why = "device returned nothing for " + cmd.Command
		}
		e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindNotApplic,
			Identity: id, Type: s.Type, Platform: platform, Command: cmd.Command, Detail: why})
		return res
	}

	art, err := e.cfg.Store.Put(info, s.Type, cmd.Command, time.Now().UTC(), []byte(out), s.Keep)
	if err != nil {
		res.Err = fmt.Errorf("store: %w", err)
		e.cfg.Emit.Send(capturerun.Event{Kind: capturerun.KindCaptureFail,
			Identity: id, Type: s.Type, Command: cmd.Command, Err: res.Err})
		return res
	}
	res.Artifact = art

	kind := capturerun.KindStored
	if art.Unchanged {
		kind = capturerun.KindUnchanged
	}
	ev := capturerun.Event{Kind: kind, Identity: id, Type: s.Type,
		Command: cmd.Command, Bytes: art.Bytes, SHA: art.SHA256, Path: art.Path}
	if pf, ok := e.parse(info, s.Type, cmd, art); ok {
		ev.ParseStatus, ev.Template = pf.Status, pf.Template
		ev.ParseScore, ev.ParseRecords = pf.Score, len(pf.Records)
		res.Parsed = &pf
	}
	e.cfg.Emit.Send(ev)
	return res
}

// parse writes the structured parse of a stored capture, when this command is
// parsed and the engine has what it needs. It runs on the stored content read
// back from disk -- on an unchanged capture that is the file that matched,
// which is what the sidecar is named for -- and a failure is logged and
// reported as no parse, never as a failed capture.
func (e *Engine) parse(info DeviceInfo, typ string, cmd Command, art Artifact) (ParsedFile, bool) {
	if cmd.TemplateHint == "" || e.cfg.Parser == nil {
		return ParsedFile{}, false
	}
	ps, ok := e.cfg.Store.(ParsedStore)
	if !ok {
		return ParsedFile{}, false
	}
	content, err := os.ReadFile(art.Path)
	if err != nil {
		e.logf("capture: %s/%s: read for parsing: %v", info.Canonical, typ, err)
		return ParsedFile{}, false
	}
	pf := BuildParsed(e.cfg.Parser, cmd.TemplateHint, content,
		filepath.Base(art.Path), art.SHA256, time.Now())
	if err := ps.PutParsed(info.Canonical, typ, pf); err != nil {
		e.logf("capture: %s/%s: write parse: %v", info.Canonical, typ, err)
		return ParsedFile{}, false
	}
	return pf, true
}
