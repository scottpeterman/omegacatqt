// internal/capturerun/params.go
//
// Capture parameters: what to capture, from what, and where it lands.
//
// This is the same job crawlrun.Params does for a crawl — the model behind a
// modal, testable without a toolkit, and the single place the CLI and the
// window agree on what a valid run looks like. Two front ends assembling their
// own parameters is how a map option went missing from the window and nothing
// said so.
//
// # What is different from a crawl
//
// A crawl takes seeds and discovers its own scope. A capture is told its
// scope: these capture types, against these devices. So there is a second list
// here that crawl has no equivalent of, and a device-list parser, because the
// list arrives as a file far more often than as a flag.
//
// The two lists are independent and the run is their cross product. Per-device
// type overrides were considered and rejected: the row key is already
// (identity, type), so the cross product is the shape the run model assumes,
// and an override list would make the third outcome ambiguous. "Not
// applicable" has to mean the platform has no command for this type. If a
// device could also be missing a row because someone deselected it, then a
// blank in the table has two explanations and the outcome that exists to stop
// a Junos box reading as a permanent startup-config failure stops meaning
// anything.
//
// # Why Types is a list of strings and not a list of Specs
//
// internal/capture imports this package for its event stream, so this package
// cannot import capture. That is not a limitation being worked around — it is
// the reason the run model is testable without a device, and it forces the
// string-to-Spec lookup into the one Build function both front ends already go
// through. Validate checks the shape; ValidateAgainst checks the names against
// whatever the caller knows.
package capturerun

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/normalize"
)

// HostKeyMode is the subset of sshcore's policy a UI may offer.
//
// Deliberately narrower than crawl's reasoning arrives at, for the same
// conclusion by a different route: a crawl meets devices it has never seen, so
// TOFU is the normal case there. A capture run works from a list of devices
// someone already administers, so Strict is the defensible default and TOFU is
// the opt-in for the first run against new gear. Insecure is absent from both,
// because it is the only mode that also stops noticing a key that CHANGED.
type HostKeyMode string

const (
	// HostKeyStrict requires the key to already be known.
	HostKeyStrict HostKeyMode = "strict"

	// HostKeyTOFU trusts an unknown key on first contact and records it. A
	// key that changes afterwards still fails closed.
	HostKeyTOFU HostKeyMode = "tofu"
)

// Params is everything a capture run needs to start.
type Params struct {
	// Devices is the list to visit, as written by whoever wrote it. These
	// are dial targets, not identities: the identity is settled at
	// connection time from the binding store or the device's own prompt.
	Devices []string `json:"devices"`

	// DeviceFile is a path whose contents are merged into Devices. Kept as
	// a field rather than resolved away so a profile can say "whatever is
	// in the list today" instead of freezing a copy of it.
	DeviceFile string `json:"device_file,omitempty"`

	// SessionFile is a session inventory to select devices from. It is a
	// path rather than a parsed tree for the same reason DeviceFile is:
	// a saved profile should mean "whatever is in the inventory today",
	// not a copy of it taken the day the profile was written.
	//
	// The file is read by the Build layer, not here. This package is
	// imported by the capture engine, so anything it imports the engine
	// inherits — and an engine that cannot be constructed without the
	// session model is one more thing between a test and the code it
	// tests.
	SessionFile string `json:"session_file,omitempty"`

	// Match are glob patterns selecting sessions out of SessionFile by
	// name or host: "agg*", "*-sw-*", "172.16.1.*".
	//
	// Empty selects NOTHING, and that is deliberate. A capture pulls a
	// running-config off every device it is handed, so a blank field
	// quietly meaning the whole inventory is the one mistake worth making
	// structurally impossible. Everything is spelled "*".
	Match []string `json:"match,omitempty"`

	// SessionKeys select sessions out of SessionFile exactly, by their
	// transport:host:port key. A view with a ticked list sends these; a
	// pattern cannot tell apart devices port-forwarded behind one address.
	// Either this or Match (or both) selects from a session file.
	SessionKeys []string `json:"session_keys,omitempty"`

	// Types are capture type names — "running-config", "inventory". Empty
	// means the caller's default set, which Build decides; it is not the
	// same as "all", and nothing here expands it.
	Types []string `json:"types,omitempty"`

	// StorePath is the capture store's root directory.
	StorePath string `json:"store_path"`

	// Concurrency is how many devices are visited at once.
	Concurrency int `json:"concurrency"`

	// ExpensiveConcurrency bounds commands the spec marks expensive across
	// the whole run. This is the number that keeps a fleet-wide
	// tech-support from being a denial of service delivered politely, so
	// it is a parameter rather than a constant — and it is separate from
	// Concurrency because raising the cheap lane is safe and raising this
	// one is not.
	ExpensiveConcurrency int `json:"expensive_concurrency"`

	// Timeout is the default per-command bound. A spec's own bound wins
	// over it, which is how one session carries both a config and a
	// tech-support without either sizing the other.
	Timeout time.Duration `json:"timeout"`

	// Domains are suffixes stripped when deriving an identity, so a device
	// written short in one list and qualified in another is one device.
	Domains []string `json:"domains,omitempty"`

	VaultPath string   `json:"vault_path,omitempty"`
	CredTags  []string `json:"cred_tags,omitempty"`

	HostKeys       HostKeyMode `json:"host_keys"`
	KnownHostsPath string      `json:"known_hosts_path,omitempty"`

	// Legacy enables old KEX and ciphers. Real gear needs it; it is an
	// algorithm policy, not a verification bypass.
	Legacy bool `json:"legacy,omitempty"`

	// NoParse stores ARP and MAC tables without parsing them. Parsing is the
	// default, so the zero value parses.
	NoParse bool `json:"no_parse,omitempty"`

	// TemplatesPath is the TextFSM template database. Empty means the one
	// in the application's configuration directory, created from the
	// shipped copy on first use.
	TemplatesPath string `json:"templates_path,omitempty"`
}

// Defaults are the values a form opens with and the CLI's flag defaults, kept
// in one place so the two cannot drift into running different captures from
// the same intent.
//
// Concurrency is lower than crawl's. A crawl reads a neighbor table; a capture
// pulls a running-config off every box at once, and the device-side cost of
// that is not comparable.
func Defaults() Params {
	return Params{
		Concurrency:          5,
		ExpensiveConcurrency: 1,
		Timeout:              60 * time.Second,
		HostKeys:             HostKeyStrict,
	}
}

// HasDeviceSource reports whether the parameters name anywhere to get devices
// from at all.
//
// It exists as one method rather than as the same three-way test written out
// in each front end, because that is exactly how a third source goes missing:
// a host that asks "are there devices or a device file" answers no for a run
// driven entirely by a session file, and quietly does something else instead.
// That happened — the session-file capture opened the store browser and never
// reached Build. Adding a source here now means adding it in one place.
func (p Params) HasDeviceSource() bool {
	return len(p.Devices) > 0 || p.DeviceFile != "" || p.SessionFile != ""
}

// ParseDevices splits a free-text field into device targets.
//
// Accepts newlines, commas, spaces, semicolons and tabs interchangeably,
// because the list arrives pasted out of a spreadsheet, a ticket, or another
// terminal and each of those uses something different. Anything after a # is a
// comment, which is what makes a hand-maintained file survive: the line that
// says why a device is skipped is the line that stops it being added back.
func ParseDevices(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.FieldsFunc(line, func(r rune) bool {
			return r == '\r' || r == ',' || r == ' ' || r == '\t' || r == ';'
		}) {
			f = strings.TrimSpace(f)
			if f == "" || seen[strings.ToLower(f)] {
				continue
			}
			seen[strings.ToLower(f)] = true
			out = append(out, f)
		}
	}
	return out
}

// LoadDeviceFile reads a device list from disk.
//
// A missing file is an error rather than an empty run. A capture that silently
// visits nothing looks identical to one where every device was fine, and the
// difference matters most on the night nobody is watching.
func LoadDeviceFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open device list: %w", err)
	}
	defer f.Close()

	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("failed to read device list: %w", err)
	}
	list := ParseDevices(b.String())
	if len(list) == 0 {
		return nil, fmt.Errorf("device list %s contains no devices", path)
	}
	return list, nil
}

// Targets is the full device list: the inline entries plus the file's, in that
// order, deduplicated. Call it rather than reading Devices directly.
func (p *Params) Targets() ([]string, error) {
	all := append([]string(nil), p.Devices...)
	if p.DeviceFile != "" {
		fromFile, err := LoadDeviceFile(p.DeviceFile)
		if err != nil {
			return nil, err
		}
		all = append(all, fromFile...)
	}
	return ParseDevices(strings.Join(all, "\n")), nil
}

// Target is one device the run will visit, after the identity rules have been
// applied to the string the list supplied.
type Target struct {
	// Dial is the string to connect to. For a CGNAT address whose name was
	// confirmed this stays the address, because the address is what is
	// known to work.
	Dial string

	// Identity is the key this device is tracked under for the run: the
	// confirmed name with any configured suffix stripped, or the original
	// string when nothing was learned. It is not the storage key — that is
	// the canonical name, settled after connecting.
	Identity string

	// Addr is the address when the entry was one.
	Addr string

	// Aliases widen the binding-store lookup. Claims, not evidence.
	Aliases []string

	// Note records a resolution worth reporting: a CGNAT address that
	// became a name, or one that did not.
	Note string
}

// ResolveTargets applies the identity rules to the device list using the live
// resolver. See ResolveTargetsWith.
func (p *Params) ResolveTargets() ([]Target, error) {
	return p.ResolveTargetsWith(normalize.DefaultResolver)
}

// ResolveTargetsWith applies the identity rules to the device list.
//
// The only lookup performed is the CGNAT rule: an address in 100.64.0.0/10 is
// reverse resolved and the PTR name adopted only if it also resolves forward.
// Everything else — names, ordinary addresses — passes through untouched and
// costs nothing.
//
// The rule is here rather than left to the engine because shared address space
// is recycled. Two devices behind different translations can wear the same
// 100.64 address in the same month, and a capture store keyed on that address
// files two boxes' configs in one history. That is the failure this is for,
// and it is silent: every run succeeds and the diff is nonsense.
//
// Names are never looked up. This lab's names have no DNS behind them at all,
// and an unresolvable name is a normal device, not a bad entry.
func (p *Params) ResolveTargetsWith(r normalize.Resolver) ([]Target, error) {
	list, err := p.Targets()
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(list))
	for _, entry := range list {
		t := Target{Dial: entry, Identity: entry}
		if _, err := netip.ParseAddr(entry); err == nil {
			t.Addr = entry
		}

		res := normalize.ResolveWith(r, entry)
		switch {
		case !res.CGNAT:
			// Nothing was looked up.
		case res.Confirmed:
			t.Identity = stripSuffixes(res.Name, p.Domains)
			t.Aliases = append(t.Aliases, res.Name, entry)
			t.Note = "CGNAT -> " + res.Name
		case res.PTR != "":
			t.Note = "CGNAT; PTR " + res.PTR + " does not resolve forward; using the address"
		default:
			t.Note = "CGNAT with no PTR; using the address"
		}

		if t.Addr == "" {
			if short := stripSuffixes(entry, p.Domains); short != entry {
				t.Identity = short
				t.Aliases = append(t.Aliases, entry)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func stripSuffixes(name string, suffixes []string) string {
	low := strings.ToLower(name)
	for _, s := range suffixes {
		s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
		if s == "" {
			continue
		}
		if strings.HasSuffix(low, "."+s) {
			return name[:len(name)-len(s)-1]
		}
	}
	return name
}

// ValidationError names the field so a form can highlight it rather than
// showing one message over the whole dialog.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string { return e.Field + ": " + e.Message }

// Normalize cleans up whitespace and casing in place. Called by Validate, and
// safe to call on its own as the user types.
func (p *Params) Normalize() {
	p.Devices = ParseDevices(strings.Join(p.Devices, "\n"))
	p.Types = cleanList(p.Types, true)
	p.Domains = cleanList(p.Domains, true)
	p.CredTags = cleanList(p.CredTags, false)
	p.DeviceFile = strings.TrimSpace(p.DeviceFile)
	p.SessionFile = strings.TrimSpace(p.SessionFile)
	// Patterns are lowercased because matching is case-insensitive, and
	// deduplicated because two spellings of one pattern would otherwise
	// look like two selections in a report.
	p.Match = cleanList(p.Match, true)
	p.SessionKeys = cleanList(p.SessionKeys, true)
	p.StorePath = strings.TrimSpace(p.StorePath)
	p.VaultPath = strings.TrimSpace(p.VaultPath)
	p.KnownHostsPath = strings.TrimSpace(p.KnownHostsPath)
	if p.HostKeys == "" {
		p.HostKeys = HostKeyStrict
	}
}

func cleanList(in []string, lower bool) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		// A suffix written ".lab.example" and one written "lab.example"
		// are the same intent; the engine wants the second form.
		v = strings.TrimPrefix(v, ".")
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// safeType matches capture.Spec's own rule for a type name. Duplicated rather
// than imported because this package cannot import capture; the spec package's
// Validate is the authority and ValidateAgainst is the check that actually
// bites, so the worst this copy can do is let a bad name through to a better
// error.
var safeType = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Validate reports every problem, not just the first, so a form can mark all
// the offending fields in one pass.
//
// Device entries are shape-checked and never resolved. A name that does not
// resolve today is still a legitimate device — this lab's names have no DNS
// behind them at all — and a validator that rejected them would reject the
// whole estate.
func (p *Params) Validate() []ValidationError {
	p.Normalize()
	var errs []ValidationError

	if !p.HasDeviceSource() {
		errs = append(errs, ValidationError{"devices",
			"a capture run needs a device list; discovery is the crawler's job"})
	}
	for _, d := range p.Devices {
		if !plausibleTarget(d) {
			errs = append(errs, ValidationError{"devices",
				fmt.Sprintf("%q is not an address or a hostname", d)})
		}
	}
	if p.DeviceFile != "" {
		if st, err := os.Stat(p.DeviceFile); err != nil {
			errs = append(errs, ValidationError{"device_file",
				fmt.Sprintf("cannot read %s: %v", p.DeviceFile, err)})
		} else if st.IsDir() {
			errs = append(errs, ValidationError{"device_file",
				p.DeviceFile + " is a directory"})
		}
	}
	if p.SessionFile != "" {
		if st, err := os.Stat(p.SessionFile); err != nil {
			errs = append(errs, ValidationError{"session_file",
				fmt.Sprintf("cannot read %s: %v", p.SessionFile, err)})
		} else if st.IsDir() {
			errs = append(errs, ValidationError{"session_file",
				p.SessionFile + " is a directory"})
		}
		// Refusing this rather than treating it as "everything" is the
		// whole reason the empty case is a rule: a session file with no
		// pattern would otherwise capture an entire hand-built
		// inventory because a field was left blank.
		if len(p.Match) == 0 && len(p.SessionKeys) == 0 {
			errs = append(errs, ValidationError{"match",
				"selecting from a session file needs at least one pattern; \"*\" is how you ask for all of it"})
		}
	}
	if len(p.Match) > 0 && p.SessionFile == "" {
		errs = append(errs, ValidationError{"match",
			"patterns select out of a session file, and none was given"})
	}
	if len(p.SessionKeys) > 0 && p.SessionFile == "" {
		errs = append(errs, ValidationError{"session_keys",
			"session keys select out of a session file, and none was given"})
	}
	for _, typ := range p.Types {
		if !safeType.MatchString(typ) {
			errs = append(errs, ValidationError{"types",
				fmt.Sprintf("%q is not a capture type name", typ)})
		}
	}
	if p.StorePath == "" {
		errs = append(errs, ValidationError{"store_path",
			"a capture with nowhere to store is a connection test"})
	}
	if p.Concurrency < 1 {
		errs = append(errs, ValidationError{"concurrency", "must be at least 1"})
	}
	if p.ExpensiveConcurrency < 1 {
		errs = append(errs, ValidationError{"expensive_concurrency", "must be at least 1"})
	}
	if p.ExpensiveConcurrency > p.Concurrency && p.Concurrency >= 1 {
		// Not fatal, but it means the expensive lane is not a limit at
		// all, which is the one thing it exists to be.
		errs = append(errs, ValidationError{"expensive_concurrency",
			"is above the device concurrency, so it bounds nothing"})
	}
	if p.Timeout <= 0 {
		errs = append(errs, ValidationError{"timeout", "must be greater than zero"})
	}
	switch p.HostKeys {
	case HostKeyStrict, HostKeyTOFU:
	case "insecure":
		errs = append(errs, ValidationError{"host_keys",
			"skipping host-key verification is not offered here; it also stops " +
				"detecting a key that changed, which is the case worth catching"})
	default:
		errs = append(errs, ValidationError{"host_keys",
			fmt.Sprintf("unknown mode %q; use strict or tofu", p.HostKeys)})
	}
	if len(p.CredTags) > 0 && p.VaultPath == "" {
		errs = append(errs, ValidationError{"cred_tags",
			"credential tags have no effect without a vault"})
	}
	return errs
}

// ValidateAgainst adds the checks that need to know which capture types exist.
// Build passes the built-in set; a form passes the same thing, so an unknown
// type is caught while the dialog is open rather than after the first dial.
func (p *Params) ValidateAgainst(known []string) []ValidationError {
	errs := p.Validate()
	if len(known) == 0 {
		return errs
	}
	set := make(map[string]bool, len(known))
	for _, k := range known {
		set[strings.ToLower(strings.TrimSpace(k))] = true
	}
	for _, typ := range p.Types {
		if !set[typ] {
			errs = append(errs, ValidationError{"types",
				fmt.Sprintf("no capture type named %q; known types are %s",
					typ, strings.Join(known, ", "))})
		}
	}
	return errs
}

// Profile is a named set of parameters.
type Profile struct {
	Name     string    `json:"name"`
	Params   Params    `json:"params"`
	LastUsed time.Time `json:"last_used,omitempty"`
}

type profileFile struct {
	Version  int       `json:"version"`
	Profiles []Profile `json:"profiles"`
}

// Profiles is the saved-parameters store.
//
// A scheduled backup is the point. A capture run is the one thing here that
// someone wants to happen the same way every night, and a dialog that opens
// empty each time cannot be that.
type Profiles struct {
	path string
	m    map[string]Profile
}

// OpenProfiles loads (or creates) the profile store. A missing file is not an
// error; a corrupt one is, so a bad write is noticed rather than quietly
// presenting an empty list that looks like a first run.
func OpenProfiles(path string) (*Profiles, error) {
	p := &Profiles{path: path, m: map[string]Profile{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return nil, fmt.Errorf("failed to read capture profiles: %w", err)
	}
	if len(raw) == 0 {
		return p, nil
	}
	var f profileFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("capture profiles file is corrupt: %w", err)
	}
	for _, pr := range f.Profiles {
		if pr.Name != "" {
			p.m[pr.Name] = pr
		}
	}
	return p, nil
}

// Names returns the profiles most-recently-used first, which is the order a
// dropdown wants.
func (p *Profiles) Names() []string {
	out := make([]string, 0, len(p.m))
	for n := range p.m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := p.m[out[i]], p.m[out[j]]
		if !a.LastUsed.Equal(b.LastUsed) {
			return a.LastUsed.After(b.LastUsed)
		}
		return out[i] < out[j]
	})
	return out
}

// Get returns a profile by name.
func (p *Profiles) Get(name string) (Profile, bool) {
	pr, ok := p.m[name]
	return pr, ok
}

// Save stores a profile under name, replacing any existing one. The params are
// normalized first so two profiles cannot differ only by whitespace.
func (p *Profiles) Save(name string, params Params) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ValidationError{"name", "a profile needs a name"}
	}
	params.Normalize()
	p.m[name] = Profile{Name: name, Params: params, LastUsed: time.Now()}
	return p.flush()
}

// Touch records that a profile was used, so it sorts to the top next time.
func (p *Profiles) Touch(name string) error {
	pr, ok := p.m[name]
	if !ok {
		return nil
	}
	pr.LastUsed = time.Now()
	p.m[name] = pr
	return p.flush()
}

// Delete removes a profile.
func (p *Profiles) Delete(name string) error {
	if _, ok := p.m[name]; !ok {
		return nil
	}
	delete(p.m, name)
	return p.flush()
}

func (p *Profiles) flush() error {
	f := profileFile{Version: 1}
	for _, n := range p.Names() {
		f.Profiles = append(f.Profiles, p.m[n])
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal capture profiles: %w", err)
	}
	if dir := filepath.Dir(p.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("failed to create profile directory: %w", err)
		}
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("failed to write capture profiles: %w", err)
	}
	if err := os.Rename(tmp, p.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to commit capture profiles: %w", err)
	}
	return nil
}

// plausibleTarget is a shape check, not a resolution attempt.
func plausibleTarget(s string) bool {
	// A target may carry a port. dial.SplitTarget is what actually reads
	// it; this only has to stop rejecting the shape, and it checks the
	// host half so a bad name with a good port is still refused.
	if host, port, ok := splitTargetPort(s); ok {
		s = host
		_ = port
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " \t/\\@") {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}

// splitTargetPort reports the host half of a target that carries a port.
//
// It is a copy of the rule in internal/dial rather than a call to it: these
// two packages are the parameter layer and the dial layer, and a run model
// importing a dialer to validate a text field would be a dependency nobody
// reading the import list would predict. The rule is small and pinned by
// tests on both sides.
func splitTargetPort(s string) (string, int, bool) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return s, 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return s, 0, false
	}
	return host, port, true
}
