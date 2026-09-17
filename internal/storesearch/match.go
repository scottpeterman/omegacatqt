package storesearch

import (
	"errors"
	"strings"
)

// ErrEmptyQuery is returned for a query with no content. It is an error rather
// than a matcher that matches everything, because "match every line of every
// config in the store" is never what an empty search box means.
var ErrEmptyQuery = errors.New("storesearch: empty query")

// Matcher decides whether one line of a captured artifact is a hit.
type Matcher interface {
	// Describe names the matcher for display — it is what a result header
	// says the search actually did, which matters as soon as there is more
	// than one kind.
	Describe() string

	// MatchLine reports whether the line matches. It is called from every
	// scan worker at once, so an implementation must be safe for
	// concurrent use and must not retain the line.
	MatchLine(line string) bool
}

// Literal matches a substring.
type Literal struct {
	needle string
	fold   bool
}

// NewLiteral builds a substring matcher.
//
// caseSensitive defaults off at every caller in the product. Configuration
// text mixes cases for the same token constantly — an interface description
// typed by one engineer and a hostname rendered by the device — so a
// case-sensitive default produces confident empty results, which is the worst
// answer a search can give.
func NewLiteral(query string, caseSensitive bool) (*Literal, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, ErrEmptyQuery
	}
	if caseSensitive {
		return &Literal{needle: q}, nil
	}
	return &Literal{needle: strings.ToLower(q), fold: true}, nil
}

// Describe implements Matcher.
func (l *Literal) Describe() string {
	if l.fold {
		return "contains " + quoted(l.needle)
	}
	return "contains " + quoted(l.needle) + ", case-sensitive"
}

// MatchLine implements Matcher.
//
// The case-insensitive path lowers the line rather than doing a fold-aware
// search, which allocates once per line. That is a deliberate first cut: it is
// correct for every alphabet rather than for ASCII only, and a local scan over
// a store of text files is measured in milliseconds. If it ever measures slow,
// the fix is a fold-aware byte search here and nowhere else.
func (l *Literal) MatchLine(line string) bool {
	if l.fold {
		return strings.Contains(strings.ToLower(line), l.needle)
	}
	return strings.Contains(line, l.needle)
}

// Needle returns the string being looked for, already folded when the matcher
// folds. A view uses it to highlight; nothing else should.
func (l *Literal) Needle() string { return l.needle }

// Folds reports whether the matcher is case-insensitive, so a highlighter can
// agree with the matcher instead of holding a second opinion about it.
func (l *Literal) Folds() bool { return l.fold }

// quoted wraps a query for display without pulling in the package of the
// Quoting matters: a query with a trailing space, or
// one that is entirely spaces, is otherwise invisible in the header.
func quoted(s string) string { return `"` + s + `"` }
