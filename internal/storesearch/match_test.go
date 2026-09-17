// internal/storesearch/match_test.go
package storesearch

import (
	"strings"
	"testing"
)

func TestAnEmptyQueryIsRefused(t *testing.T) {
	for _, q := range []string{"", "   ", "\t\n"} {
		if _, err := NewLiteral(q, false); err != ErrEmptyQuery {
			t.Errorf("NewLiteral(%q) = %v, want ErrEmptyQuery — an empty box must not mean every line of every config", q, err)
		}
	}
}

// Configuration text mixes cases for one token constantly, so folding is the
// default and case-sensitivity is the opt-in.
func TestLiteralFoldsUnlessAskedNotTo(t *testing.T) {
	folding, err := NewLiteral("BGP", false)
	if err != nil {
		t.Fatal(err)
	}
	if !folding.MatchLine("router bgp 65001") {
		t.Error("case-insensitive matcher missed a differently-cased line")
	}

	exact, err := NewLiteral("BGP", true)
	if err != nil {
		t.Fatal(err)
	}
	if exact.MatchLine("router bgp 65001") {
		t.Error("case-sensitive matcher matched a differently-cased line")
	}
	if !exact.MatchLine("router BGP 65001") {
		t.Error("case-sensitive matcher missed an exact match")
	}
}

func TestDescribeQuotesTheQuery(t *testing.T) {
	m, err := NewLiteral("  vlan 10  ", false)
	if err != nil {
		t.Fatal(err)
	}
	// The query is trimmed, and Describe quotes what is actually being
	// looked for — otherwise a query that differs only in whitespace
	// renders identically to one that does not.
	if got := m.Describe(); !strings.Contains(got, `"vlan 10"`) {
		t.Errorf("Describe() = %q", got)
	}
}
