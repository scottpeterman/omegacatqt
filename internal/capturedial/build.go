// internal/capturedial/build.go
//
// One place that turns capturerun.Params into a configured capture engine.
//
// The crawl side already has this and already learned why it needs it: two
// front ends assembling their own crawler drifted, and the symptom was a map
// with fewer links and nothing saying which option went missing. A capture has
// the same shape of failure available to it and a worse version of it — a run
// assembled slightly differently stores a slightly different set of artifacts,
// and nobody looks at a backup until the day they need one.
//
// So Build exists before the second front end does, not after. cmd/capture is
// a harness with no features of its own; whatever window comes later goes
// through this same function.
//
// # Why this is not in internal/capture
//
// The engine imports dial, credres and netexec and stops there. Opening a
// vault pulls in vaultcli and the OS keyring behind it, and an engine that
// cannot be constructed without a keyring is an engine that cannot be tested
// without one. Same reasoning that keeps a toolkit out of it, and the same
// split crawldial already represents.
package capturedial

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/credres"
	"github.com/scottpeterman/omegacatqt/internal/dial"
	"github.com/scottpeterman/omegacatqt/internal/netexec"
	"github.com/scottpeterman/omegacatqt/internal/normalize"
	"github.com/scottpeterman/omegacatqt/internal/sshcore"
	"github.com/scottpeterman/omegacatqt/internal/tfsmfire"
	"github.com/scottpeterman/omegacatqt/internal/vault"
	"github.com/scottpeterman/omegacatqt/internal/vaultcli"
)

// StaticCreds is the no-vault path: one credential for every device.
type StaticCreds struct {
	Username string
	Password string
	KeyPath  string
}

// Built is an engine and the things a caller still needs afterwards.
type Built struct {
	Engine *capture.Engine
	Store  *capture.FileStore

	// Devices is the resolved device list, in the order it will be
	// visited. Returned rather than kept private because a caller wants to
	// print what it is about to do — and because a CGNAT address that
	// became a name is a decision worth showing before the run, not after.
	Devices []capture.Device

	// Notes are per-device resolution notes, keyed by the identity in
	// Devices. Only devices that had something to report appear.
	Notes map[string]string

	// Skipped are sessions a pattern matched that capture cannot visit —
	// a telnet console, a session with no host. Reported rather than
	// dropped: a pattern that matched fourteen sessions and captured nine
	// has to say what happened to the other five, or the next question is
	// whether the pattern was wrong.
	Skipped []Skipped

	// Specs is what Types resolved to.
	Specs []capture.Spec

	Bindings *credres.FileBindings
	Resolver *credres.Resolver

	// CredNames maps credential ID to display name, for reporting. Never
	// secret material.
	CredNames map[string]string

	// Close releases the vault. Always non-nil.
	Close func()
}

// Options are the things Params deliberately does not carry: secrets, a
// bastion, and where output goes.
type Options struct {
	// Static is used when Params.VaultPath is empty.
	Static StaticCreds

	// Vault is an already-unlocked vault to use instead of opening
	// Params.VaultPath.
	//
	// A GUI unlocks once and has nowhere to prompt for a master password
	// afterwards. Without this, Build calls vaultcli.Open on the run's own
	// goroutine, that falls through to a terminal read nobody is watching,
	// and the run blocks forever before dialing anything — which looks
	// exactly like a device that will not answer.
	//
	// Params.VaultPath is still read: it names where the bindings live and
	// it is recorded with the run. It is simply not opened.
	Vault *vault.Vault

	// Jump is an optional bastion.
	Jump *sshcore.JumpConfig

	// BindingsPath overrides the default, which sits beside the vault.
	BindingsPath string

	// DNS resolves the CGNAT rule. Nil uses the live resolver.
	DNS normalize.Resolver

	// Log receives capture progress lines. Nil discards them.
	Log func(string, ...any)

	// CredLog receives credential-resolution lines. Nil discards them.
	CredLog func(string, ...any)

	// Announce receives the human-readable host-key line that accompanies a
	// first-contact event. Nil writes it to stderr, which is what a CLI
	// wants; a GUI has no terminal and routes it to its log instead.
	Announce func(string, ...any)

	// Emit receives structured events. Nil is fine and costs nothing — it
	// is what makes this usable from a CLI that only wants the log.
	Emit capturerun.Emit

	// Parser replaces the template database Params would open. A caller
	// that runs many captures opens the database once and passes it here.
	Parser capture.Parser
}

// DefaultTypes is what a run captures when Params.Types is empty.
//
// One type, and the cheap one. The first use of this tool is config backup, so
// defaulting to the running config is defaulting to the thing someone came
// here for. Defaulting to everything would put a tech-support on every box in
// the estate because a field was left blank, and "the default did it" is not a
// thing to say to a network on a Monday.
var DefaultTypes = []string{capture.RunningConfig.Type}

// HostKeyEmitter adapts dial's first-contact callback to a capturerun event,
// so a TOFU acceptance lands in the decisions list instead of scrolling past
// on stderr.
//
// This is why internal/dial does not import either run package: a package
// named "dial" depending on one named "crawlrun" is a dependency nobody would
// predict from the name, and capture would have inherited it.
func HostKeyEmitter(emit capturerun.Emit) dial.NewHostKeyFunc {
	return func(host, keyType, fingerprint string) {
		emit.Send(capturerun.Event{
			Kind:     capturerun.KindHostKeyNew,
			Identity: host,
			Detail:   keyType + " " + fingerprint,
		})
	}
}

// CredObserver adapts the resolver's credential outcomes to capturerun events.
//
// The twin of HostKeyEmitter, and it closes the same gap for a worse case.
// credres.Config.Emit was a crawlrun.Emit (removed in omegacat), so until this
// existed a capture got NO credential events at all: capturerun declared KindAuthOK, KindAuthReject
// and KindCredParked "mirroring crawlrun" and nothing ever sent one. The
// symptom was a run failing on every device with a handshake error and no way
// to tell WHICH of the vault's credentials had been offered — the answer
// existed only as a stderr line, behind a verbose flag, in a GUI with no
// terminal attached.
//
// AuthReject is the one that earns this. A rejection names the credential and
// says whether it came from a pin, a promotion or the ladder, which is the
// difference between "the credential this session names is wrong" and "the
// session's credential was never tried".
func CredObserver(emit capturerun.Emit) credres.Observer {
	return credres.Observer{
		AuthOK: func(identity, cred, reason string) {
			emit.Send(capturerun.Event{
				Kind:     capturerun.KindAuthOK,
				Identity: identity,
				Detail:   cred + " (" + reason + ")",
			})
		},
		AuthReject: func(identity, cred, reason string) {
			emit.Send(capturerun.Event{
				Kind:     capturerun.KindAuthReject,
				Identity: identity,
				Detail:   cred + " (" + reason + ") was rejected",
			})
		},
		CredParked: func(cred, detail string) {
			// No identity: this is about the credential, not a
			// device, and every device after it silently never sees
			// that credential again.
			emit.Send(capturerun.Event{
				Kind:   capturerun.KindCredParked,
				Detail: cred + ": " + detail,
			})
		},
	}
}

// KnownTypes is the list of capture type names Params can be validated
// against. Sorted, so an error message reads the same every time.
func KnownTypes() []string {
	specs := capture.Builtin()
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Type)
	}
	sort.Strings(out)
	return out
}

// SpecsFor turns type names into specs.
//
// This is the lookup that cannot live in capturerun: internal/capture imports
// capturerun for its event stream, so capturerun can never import capture, and
// the string-to-spec step has to happen somewhere that imports both. Here is
// that somewhere, and it is already the place both front ends go through.
func SpecsFor(types []string) ([]capture.Spec, error) {
	if len(types) == 0 {
		types = DefaultTypes
	}
	out := make([]capture.Spec, 0, len(types))
	for _, t := range types {
		s, ok := capture.Lookup(t)
		if !ok {
			return nil, fmt.Errorf("no capture type named %q; known types are %s",
				t, strings.Join(KnownTypes(), ", "))
		}
		out = append(out, s)
	}
	return out, nil
}

// Devices turns the parameters' device list into engine devices, applying the
// CGNAT rule on the way.
//
// The identity carried here is the RUN identity — the key every event in the
// run is filed under. It is not the storage key; that is the canonical name,
// settled after connecting, from the binding store or the device's own prompt.
// Keeping those two apart is what stops one device appearing as two rows.
func Devices(p capturerun.Params, r normalize.Resolver) ([]capture.Device, map[string]string, []Skipped, error) {
	if r == nil {
		r = normalize.DefaultResolver
	}
	targets, err := p.ResolveTargetsWith(r)
	if err != nil {
		return nil, nil, nil, err
	}
	out := make([]capture.Device, 0, len(targets))
	notes := map[string]string{}
	for _, t := range targets {
		out = append(out, capture.Device{
			Target:   t.Dial,
			Identity: t.Identity,
			Addr:     t.Addr,
			Aliases:  t.Aliases,
		})
		if t.Note != "" {
			notes[t.Identity] = t.Note
		}
	}

	// The session tree contributes after the explicit list, so a device
	// named on the command line keeps its place at the front of the run
	// and a pattern that also matches it does not visit it twice.
	fromTree, treeNotes, skipped, err := SessionDevices(p, r)
	if err != nil {
		return nil, nil, nil, err
	}
	out = mergeDevices(out, fromTree)
	for id, note := range treeNotes {
		if _, have := notes[id]; !have {
			notes[id] = note
		}
	}
	return out, notes, skipped, nil
}

// Build validates the parameters and assembles a capture engine from them.
//
// Validation happens here rather than at the caller so both front ends refuse
// the same things. The returned error joins every validation problem, since a
// form wants to mark all the bad fields at once and a CLI wants to print them
// all rather than one per run.
func Build(p capturerun.Params, opts Options) (*Built, error) {
	if errs := p.ValidateAgainst(KnownTypes()); len(errs) > 0 {
		msg := "invalid capture parameters:"
		for _, e := range errs {
			msg += "\n  " + e.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}

	specs, err := SpecsFor(p.Types)
	if err != nil {
		return nil, err
	}

	// Nothing to authenticate with is worth catching before the fleet is
	// dialed rather than after. dial.Static offers only the username,
	// password and key it is handed — it does not enable agent auth — so
	// with none of those the handshake falls through to
	// keyboard-interactive, which has no handler, and every device on the
	// list fails with an SSH-layer message that describes the symptom
	// rather than the cause.
	if p.VaultPath == "" && opts.Static.Password == "" && opts.Static.KeyPath == "" {
		msg := "no credentials: supply a vault, a private key, or a password " +
			"(every device would fail the handshake otherwise)"
		// ocvault finds the vault with no path given, so a person who has
		// one has every reason to expect this to as well. Naming it beats
		// silently adopting it: a run that quietly picks up a vault is a
		// run that quietly picks up whichever credentials happen to be in
		// it.
		if def := vaultcli.DefaultPath(); def != "" {
			if _, err := os.Stat(def); err == nil {
				msg += fmt.Sprintf("\n  a vault exists at %s — pass -vault %s", def, def)
			}
		}
		return nil, fmt.Errorf("%s", msg)
	}
	devices, notes, skipped, err := Devices(p, opts.DNS)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("the device list resolved to nothing to capture")
	}

	store, err := capture.OpenFileStore(p.StorePath)
	if err != nil {
		return nil, err
	}

	announce := opts.Announce
	if announce == nil {
		announce = dial.Stderr("capture")
	}

	policy := sshcore.HostKeyStrict
	if p.HostKeys == capturerun.HostKeyTOFU {
		policy = sshcore.HostKeyTOFU
	}
	base := dial.BaseConfig{
		OnNewHostKey:   HostKeyEmitter(opts.Emit),
		Announce:       announce,
		Legacy:         p.Legacy,
		HostKeys:       policy,
		Jump:           opts.Jump,
		KnownHostsPath: p.KnownHostsPath,
	}

	credLog := opts.CredLog
	if credLog == nil {
		credLog = func(string, ...any) {}
	}

	out := &Built{
		Store:   store,
		Devices: devices,
		Notes:   notes,
		Skipped: skipped,
		Specs:   specs,
		Close:   func() {},
	}

	var dialFn dial.Func
	if p.VaultPath == "" {
		dialFn = dial.Static(base, opts.Static.Username, opts.Static.Password, opts.Static.KeyPath)
	} else {
		v := opts.Vault
		if v == nil {
			opened, err := vaultcli.Open(p.VaultPath)
			if err != nil {
				return nil, err
			}
			// Lock only what we opened. A caller-supplied vault
			// outlives this run, and locking it here would leave
			// the next one without credentials.
			out.Close = func() { opened.Lock() }
			v = opened
		}

		bp := opts.BindingsPath
		if bp == "" {
			bp = vaultcli.BindingsPath(p.VaultPath)
		}
		bindings, err := credres.OpenFileBindings(bp)
		if err != nil {
			out.Close()
			return nil, err
		}
		out.Bindings = bindings

		out.Resolver = credres.New(v, bindings, credres.Config{
			Log:      credLog,
			Observer: CredObserver(opts.Emit),
		})
		dialFn = dial.NewVault(out.Resolver, base, p.CredTags, credLog).Dial

		out.CredNames = map[string]string{}
		if metas, err := v.List(); err == nil {
			for _, m := range metas {
				out.CredNames[m.ID] = m.Name
			}
		}
	}

	// A nil Bindings is a working configuration — the engine falls back to
	// the device's prompt — but it means capture stops contributing to
	// identity, and a device list is exactly the case where nothing else
	// ever will.
	var resolver capture.Resolver
	if out.Bindings != nil {
		resolver = out.Bindings
	}

	parser, err := parserFor(p, specs, opts)
	if err != nil {
		out.Close()
		return nil, err
	}

	eng, err := capture.New(capture.Config{
		Dial:                 dialFn,
		Store:                store,
		Specs:                specs,
		Bindings:             resolver,
		Concurrency:          p.Concurrency,
		ExpensiveConcurrency: p.ExpensiveConcurrency,
		SessionOpts:          netexec.Options{CommandTimeout: p.Timeout},
		Log:                  opts.Log,
		Emit:                 opts.Emit,
		Parser:               parser,
	})
	if err != nil {
		out.Close()
		return nil, err
	}
	out.Engine = eng
	return out, nil
}

// parserFor opens the template database when this run has anything to parse.
//
// Only then: loading the database compiles every template once, a few seconds,
// and a config backup has no reason to pay it. And a database that will not
// open fails the run rather than quietly dropping parsing -- the person asked
// for tables they can read, and a run that stores them unparsed with no word
// about why is the silent partial answer this tool avoids everywhere else.
func parserFor(p capturerun.Params, specs []capture.Spec, opts Options) (capture.Parser, error) {
	if p.NoParse || !NeedsParser(specs) {
		return nil, nil
	}
	if opts.Parser != nil {
		return opts.Parser, nil
	}
	path, err := TemplatesPath(p)
	if err != nil {
		return nil, err
	}
	eng, err := tfsmfire.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w (disable parsing to capture without it)", err)
	}
	return eng, nil
}

// NeedsParser reports whether any of specs parses its output.
func NeedsParser(specs []capture.Spec) bool {
	for _, s := range specs {
		for _, c := range s.Commands {
			if c.TemplateHint != "" {
				return true
			}
		}
	}
	return false
}

// TemplatesPath is the template database a run uses: Params.TemplatesPath, or
// the one in the configuration directory, created from the shipped copy the
// first time it is needed.
func TemplatesPath(p capturerun.Params) (string, error) {
	if p.TemplatesPath != "" {
		return p.TemplatesPath, nil
	}
	dir := vaultcli.AppDir()
	if dir == "" {
		return "", fmt.Errorf("parsing needs a template database and there is no configuration directory; pass a templates path or disable parsing")
	}
	path, _, err := tfsmfire.EnsureDB(dir)
	return path, err
}
