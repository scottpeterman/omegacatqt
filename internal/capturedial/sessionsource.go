// internal/capturedial/sessionsource.go
//
// Selecting capture devices out of the session tree.
//
// A device list beside a session tree is a second inventory, and a second
// inventory is one that is wrong. The tree is already the organised list of
// what exists and how to reach it, so pointing a capture at it with a pattern
// — agg*, *-sw-* — is both less typing and one fewer thing to keep in step.
//
// # Why this is here and not in capturerun
//
// internal/capture imports capturerun, so anything capturerun imports the
// engine inherits. Putting the session model there would mean an engine that
// cannot be constructed without the inventory format, which is the same
// constraint that already forced Params.Types to be []string rather than
// []Spec. Build is the one place both front ends go through and the one place
// that is allowed to know about every format, so the expansion happens here.
//
// # What a selected session becomes
//
// The node's host is the dial target, with its port appended when it has one.
// The node's NAME is the run identity when it has one, because the tree is
// hand-organised and the name in it is what its owner calls the device — a
// crawl-imported name or a hand-typed one both beat an address for reading a
// report. The address stays the thing that is dialed, since it is what is
// known to work.
//
// The CGNAT rule still runs on the host, because a session file is exactly
// where a 100.64 address gets written down and left, and shared address space
// is recycled — two devices wearing one address file their configs into one
// history and every run looks fine.
//
// # What is refused, loudly
//
// Capture dials SSH. A telnet or serial session cannot be captured, and a
// session with no host cannot be dialed. Those are reported as skipped with a
// reason rather than dropped: a pattern that matched fourteen sessions and
// captured nine should say which five it could not use and why, or the next
// question is whether the pattern was wrong.
package capturedial

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/normalize"
	"github.com/scottpeterman/omegacatqt/internal/sessions"
)

// Skipped is one session a pattern matched but capture cannot visit.
type Skipped struct {
	Name   string
	Folder string
	Reason string
}

func (s Skipped) String() string {
	where := s.Name
	if s.Folder != "" {
		where = s.Folder + "/" + s.Name
	}
	return where + ": " + s.Reason
}

// SessionDevices reads the session file named in the parameters and returns
// the devices its patterns select.
//
// A file with no matches is NOT an error here. The caller knows whether
// anything else contributed devices, and a pattern that matched nothing
// alongside an explicit -device list is a different situation from one that
// leaves the run with nothing to do.
func SessionDevices(p capturerun.Params, r normalize.Resolver) ([]capture.Device, map[string]string, []Skipped, error) {
	if strings.TrimSpace(p.SessionFile) == "" {
		return nil, nil, nil, nil
	}
	if r == nil {
		r = normalize.DefaultResolver
	}
	tree, err := sessions.LoadFile(p.SessionFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("session file %s: %w", p.SessionFile, err)
	}

	var (
		devices []capture.Device
		skipped []Skipped
	)
	notes := map[string]string{}
	for _, sel := range selectSessions(tree, p.Match, p.SessionKeys) {
		n := sel.Node
		label := n.Label()
		host := strings.TrimSpace(n.Host)

		if n.Transport != "" && n.Transport != sessions.TransportSSH {
			skipped = append(skipped, Skipped{label, sel.Folder,
				fmt.Sprintf("%s session; capture connects over SSH", n.Transport)})
			continue
		}
		if host == "" {
			skipped = append(skipped, Skipped{label, sel.Folder, "no host to dial"})
			continue
		}

		d, note := sessionDevice(n, host, p.Domains, r)
		devices = append(devices, d)
		if note != "" {
			notes[d.Identity] = note
		}
	}
	if len(notes) == 0 {
		notes = nil
	}
	return devices, notes, skipped, nil
}

// selectSessions is every session the patterns or the keys pick, in tree
// order, each once.
func selectSessions(tree sessions.Tree, patterns, keys []string) []sessions.Selection {
	byPattern := tree.Select(patterns)
	if len(keys) == 0 {
		return byPattern
	}
	byKey := tree.SelectKeys(keys)
	if len(byPattern) == 0 {
		return byKey
	}
	picked := map[string]bool{}
	for _, s := range append(byPattern, byKey...) {
		picked[s.Folder+"\x00"+s.Node.Key()] = true
	}
	var out []sessions.Selection
	seen := map[string]bool{}
	for _, f := range tree.Folders {
		for _, n := range f.Sessions {
			k := n.Key()
			if !picked[f.Name+"\x00"+k] || (k != "" && seen[k]) {
				continue
			}
			if k != "" {
				seen[k] = true
			}
			out = append(out, sessions.Selection{Folder: f.Name, Node: n})
		}
	}
	return out
}

// sessionDevice applies the identity rules to one node.
func sessionDevice(n sessions.Node, host string, domains []string, r normalize.Resolver) (capture.Device, string) {
	d := capture.Device{
		Target:   host,
		Identity: host,
		// The session names the credential its owner uses for this
		// device. Dropping it was why a capture over a mixed estate
		// walked the ladder from the top on every box and failed on the
		// ones whose credential is not the one that happens to rank
		// first.
		Credential: strings.TrimSpace(n.Credential),
		// A session's platform is a person's word, its device_type a
		// crawl's guess, and legacy what that one box needs. All three
		// come along; capture decides what each is allowed to do.
		Legacy:       n.LegacyAlgorithms,
		Platform:     strings.TrimSpace(n.Platform),
		PlatformHint: strings.TrimSpace(n.DeviceType),
	}
	// A port belongs on the dial target and nowhere else. It is not part
	// of the identity: the same device reached on 22 today and through a
	// console server tomorrow is one device, and filing its configs under
	// two keys because the route changed is the failure the binding store
	// exists to prevent.
	if n.Port != 0 && n.Port != 22 {
		d.Target = joinHostPort(host, n.Port)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		d.Addr = host
	}

	var note string
	res := normalize.ResolveWith(r, host)
	switch {
	case !res.CGNAT:
		// Nothing was looked up.
	case res.Confirmed:
		d.Identity = stripDomains(res.Name, domains)
		d.Aliases = append(d.Aliases, res.Name, host)
		note = "CGNAT -> " + res.Name
	case res.PTR != "":
		note = "CGNAT; PTR " + res.PTR + " does not resolve forward; using the address"
	default:
		note = "CGNAT with no PTR; using the address"
	}

	// The name the tree carries wins over an address, and over a PTR: the
	// person filed it, and a report reading lab-agg1 rather than 172.16.1.11
	// is the reason to select from the tree at all. The PTR name is kept
	// as an alias so the binding store can still match on it.
	//
	// Except when the name is not a name. sessions.Normalize fills an
	// empty Name with the node's own Target — user@host:port — so a
	// session somebody never named comes back from the file carrying a
	// connection string where its name should be. Taking that literally
	// puts the port and the username into the identity and files the same
	// device under a second key the day either one changes. A name equal
	// to what this node connects to is not a name.
	if name := stripDomains(strings.TrimSpace(n.Name), domains); name != "" && !isGeneratedName(n, name) {
		if d.Identity != name {
			d.Aliases = appendUnique(d.Aliases, d.Identity)
		}
		d.Identity = name
		d.Aliases = appendUnique(d.Aliases, host, strings.TrimSpace(n.Name))
	}
	d.Aliases = dropValue(d.Aliases, d.Identity)
	return d, note
}

// isGeneratedName reports whether a node's Name is really its connection
// string, which is what sessions.Normalize leaves behind for a session nobody
// named.
func isGeneratedName(n sessions.Node, name string) bool {
	return strings.EqualFold(name, n.Target())
}

func joinHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // bare IPv6
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// stripDomains removes a configured suffix, matching what ResolveTargetsWith
// does for the device list, so a device written short in one place and
// qualified in the other is one device.
func stripDomains(name string, domains []string) string {
	low := strings.ToLower(name)
	for _, s := range domains {
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

func appendUnique(list []string, values ...string) []string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		found := false
		for _, have := range list {
			if strings.EqualFold(have, v) {
				found = true
				break
			}
		}
		if !found {
			list = append(list, v)
		}
	}
	return list
}

func dropValue(list []string, v string) []string {
	out := list[:0]
	for _, item := range list {
		if !strings.EqualFold(item, v) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeDevices appends the second list to the first, dropping any device whose
// dial target is already present.
//
// The dial target is the key rather than the identity because that is what
// determines whether the same box is visited twice. A device named in -device
// and also matched by a pattern is one visit, and the first mention wins so
// the explicit list keeps its place at the front of the run.
func mergeDevices(first, second []capture.Device) []capture.Device {
	seen := make(map[string]bool, len(first))
	for _, d := range first {
		seen[strings.ToLower(d.Target)] = true
	}
	out := first
	for _, d := range second {
		k := strings.ToLower(d.Target)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	return out
}

// SkippedLines renders the skip list for a report, in a stable order.
func SkippedLines(skipped []Skipped) []string {
	out := make([]string, 0, len(skipped))
	for _, s := range skipped {
		out = append(out, s.String())
	}
	sort.Strings(out)
	return out
}
