// internal/sessions/select.go
//
// Picking sessions out of the tree by pattern.
//
// The inventory is the list of devices somebody has already organised by hand,
// which makes it the natural thing to point a capture at: "every aggregation
// switch" is a shape somebody can type, where a device list is a file that has
// to be maintained beside the tree and goes stale the day a device is added to
// one and not the other.
//
// The rules are here rather than in whichever front end asks, for the same
// reason the rest of this package is: what a pattern matches is a rule, and a
// rule that needs a display to test stops being tested.
//
// # What a pattern matches
//
// The session's NAME and its HOST, either one. Both because a tree built by
// importing a crawl names devices by whatever they reported while a
// hand-entered one is often addresses, and somebody typing 172.16.1.* means it.
//
// A dotted name also matches on its first label, so lab-agg1 finds
// lab-agg1.site1 without the person having to know which form the tree
// happens to hold. That is the same suffix tolerance the identity rules already apply
// everywhere else; a pattern that had to match the stored string exactly would
// be a pattern that only works if you already looked.
//
// Matching is case-insensitive and the wildcards are path.Match's: * ? and
// [a-z]. path rather than filepath so a pattern behaves the same on Windows,
// where filepath's separator would give * a different meaning.
//
// # Why an empty pattern list selects nothing
//
// A capture pulls a running-config off every device it is given. An empty
// field selecting the whole inventory is the one mistake worth making
// structurally impossible, so it selects nothing and the caller reports that.
// Everything is spelled '*', which is a thing somebody meant to type.
package sessions

import (
	"path"
	"strings"
)

// Selection is one session a pattern picked, with the folder it came from.
//
// The folder is not part of matching; it is here so a report can say where
// each device came from, which is what makes a surprising match explainable.
type Selection struct {
	Folder string
	Node   Node
}

// Select returns every session matching any of the patterns, in tree order,
// with a session that matches more than one pattern appearing once.
//
// Order is the tree's own — folders top to bottom, sessions within a folder in
// the order they are filed. A person who organised the inventory gets the
// devices back in the order they organised them, and a run's output is
// therefore stable across invocations.
func (t Tree) Select(patterns []string) []Selection {
	pats := cleanPatterns(patterns)
	if len(pats) == 0 {
		return nil
	}
	var out []Selection
	seen := map[string]bool{}
	for _, f := range t.Folders {
		for _, n := range f.Sessions {
			if !matchesAny(pats, n) {
				continue
			}
			// Key is transport:host:port, so the same device filed in
			// two folders is one device. A node with no address at all
			// has an empty key and can never dedupe against another,
			// which is the honest answer: nothing identifies it.
			if k := n.Key(); k != "" {
				if seen[k] {
					continue
				}
				seen[k] = true
			}
			out = append(out, Selection{Folder: f.Name, Node: n})
		}
	}
	return out
}

// SelectKeys returns the sessions whose Key is one of keys, in tree order, each
// once.
//
// This is how a view that lets somebody tick devices selects exactly those
// devices. A pattern cannot: it is tried against host and name but never the
// port, so ticking one of several devices port-forwarded behind one address
// would select all of them. A key is transport:host:port, the same identity
// Import deduplicates on, so it names one entry and nothing else.
func (t Tree) SelectKeys(keys []string) []Selection {
	want := map[string]bool{}
	for _, k := range keys {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			want[k] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	var out []Selection
	seen := map[string]bool{}
	for _, f := range t.Folders {
		for _, n := range f.Sessions {
			k := n.Key()
			if k == "" || !want[k] || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, Selection{Folder: f.Name, Node: n})
		}
	}
	return out
}

// MatchNode reports whether one pattern picks one session.
func MatchNode(pattern string, n Node) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return false
	}
	return matchOne(p, n)
}

func matchesAny(pats []string, n Node) bool {
	for _, p := range pats {
		if matchOne(p, n) {
			return true
		}
	}
	return false
}

func matchOne(pat string, n Node) bool {
	for _, cand := range candidates(n) {
		if cand == "" {
			continue
		}
		// path.Match only errors on a malformed pattern, and a
		// malformed pattern matches nothing rather than failing the
		// run: one bad entry in a list of six should cost that entry.
		if ok, err := path.Match(pat, cand); err == nil && ok {
			return true
		}
	}
	return false
}

// candidates are the strings a pattern is tried against, lowercased.
func candidates(n Node) []string {
	name := strings.ToLower(strings.TrimSpace(n.Name))
	host := strings.ToLower(strings.TrimSpace(n.Host))
	out := []string{name, host}
	if i := strings.Index(name, "."); i > 0 {
		out = append(out, name[:i])
	}
	// A host written as a name gets the same short form. An address does
	// not: the first label of 172.16.1.107 is "172", and a pattern matching
	// that would be a pattern nobody could predict.
	if !looksLikeAddress(host) {
		if i := strings.Index(host, "."); i > 0 {
			out = append(out, host[:i])
		}
	}
	return out
}

func looksLikeAddress(s string) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, ":") {
		return true // IPv6, or something with a port; either way not a name
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

func cleanPatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
