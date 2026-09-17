// internal/tfsmfire/engine.go
//
// A Go port of tfsm-fire: hand it raw CLI output and a hint, and it tries every
// template the hint selects, scores what each one parsed, and returns the best.
// "The output selects the template."
//
// It is a PORT, not a reinterpretation. The selection it makes and the score it
// gives are required to match the Python engine (netlapse's
// parser/tfsm_fire.py) exactly, and parity_test.go holds it to that against
// results recorded from the Python engine. That includes the engine's wrong
// picks: when a template is too loose and wins output that is not its own, the
// fix is a better template (which is what the template tooling is for), not a
// scoring change here that would make this engine disagree with every other
// tool reading the same database.
//
// # Hints
//
// The hint narrows the candidates before scoring, and how much it narrows is
// the whole tuning knob:
//
//	juniper_junos                        the normal hint: one platform's
//	                                     templates, and the scorer picks
//	juniper_junos_show_lldp_neighbors    a command stem, for a command with
//	                                     several templates (version variants:
//	                                     _neighbors, _neighbors2, ...)
//
// A hint that names exactly one template leaves nothing to score and defeats
// the engine. A hint with only the vendor ("juniper") admits every platform of
// that vendor.
//
// The filter is the Python one: split the hint on '_' (after '-' becomes '_'),
// drop terms of two characters or fewer, and require every remaining term to
// appear in the template name, case-insensitively for ASCII -- SQLite LIKE
// semantics, reproduced in memory.
//
// # Concurrency
//
// An Engine is safe for concurrent use. It holds template SOURCE, never a
// compiled gotextfsm.TextFSM: gotextfsm keeps per-parse working state inside
// the template's own Values map, so a compiled template is scratch space, and
// sharing one across goroutines is the data race that crashed Pathfinder's
// crawler (see internal/tfsm in that project). Each parse compiles its own.
// Python recompiles per candidate too, so this costs nothing in fidelity.
package tfsmfire

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sirikothe/gotextfsm"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Template is one row of the templates table.
type Template struct {
	ID      int64  // the id column; may be 0 where the row has none
	RowID   int64  // SQLite rowid: the order the Python engine tries them in
	Name    string // cli_command, e.g. "arista_eos_show_ip_arp"
	Sample  string // cli_content: sample output the template was written for
	Source  string // "ntc", "custom", ... or empty
	Content string // textfsm_content

	// Header is the Value names in template order, for a table's columns.
	Header []string

	// CompileErr is why gotextfsm refused the template, or empty. A template
	// that does not compile is never a candidate -- the Python engine skips
	// one that raises the same way -- but it stays listed, so a template
	// tool can show it and its error.
	CompileErr string
}

// Record is one parsed row. A value is a string, or a []string for a List
// Value.
type Record map[string]any

// Match is the result of FindBest.
type Match struct {
	Template string   // the winning template's name; empty when nothing scored
	Header   []string // its Value names in template order
	Records  []Record
	Score    float64
	Tried    int // candidates the hint selected (compiling or not)
}

// Engine holds a template database loaded into memory.
type Engine struct {
	path string

	mu        sync.RWMutex
	templates []*Template
}

// Open loads every template from the SQLite database at path, read-only.
func Open(path string) (*Engine, error) {
	e := &Engine{path: path}
	if err := e.Reload(); err != nil {
		return nil, err
	}
	return e, nil
}

// Path is the database the engine was opened on.
func (e *Engine) Path() string { return e.path }

// Reload re-reads the database, for after the templates have been edited.
// Matches already returned are unaffected.
func (e *Engine) Reload() error {
	ts, err := loadTemplates(e.path)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.templates = ts
	e.mu.Unlock()
	return nil
}

func loadTemplates(path string) ([]*Template, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("tfsmfire: no database path")
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("tfsmfire: open %s: %w", path, err)
	}
	defer db.Close()

	// No ORDER BY in the Python query; a plain table scan returns rowid
	// order, and ties go to whichever template came first, so the order is
	// part of the result. Stated explicitly here rather than inherited.
	rows, err := db.Query(`SELECT rowid, COALESCE(id, 0), COALESCE(cli_command, ''),
		COALESCE(cli_content, ''), COALESCE(textfsm_content, ''), COALESCE(source, '')
		FROM templates ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("tfsmfire: read templates from %s: %w", path, err)
	}
	defer rows.Close()

	var out []*Template
	for rows.Next() {
		t := &Template{}
		if err := rows.Scan(&t.RowID, &t.ID, &t.Name, &t.Sample, &t.Content, &t.Source); err != nil {
			return nil, fmt.Errorf("tfsmfire: read template row: %w", err)
		}
		t.Header = headerOf(t.Content)
		var fsm gotextfsm.TextFSM
		if err := fsm.ParseString(t.Content); err != nil {
			t.CompileErr = err.Error()
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tfsmfire: read templates from %s: %w", path, err)
	}
	return out, nil
}

// Templates returns every template, in rowid order. The slice is a copy; the
// templates are shared and must not be modified.
func (e *Engine) Templates() []*Template {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]*Template(nil), e.templates...)
}

// Filter returns the templates a hint selects, in rowid order. An empty hint
// selects every template.
func (e *Engine) Filter(hint string) []*Template {
	terms := filterTerms(hint)
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []*Template
	for _, t := range e.templates {
		if matchesTerms(t.Name, terms) {
			out = append(out, t)
		}
	}
	return out
}

// Template returns one template by exact name.
func (e *Engine) Template(name string) (*Template, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, t := range e.templates {
		if t.Name == name {
			return t, true
		}
	}
	return nil, false
}

// FindBest tries every template the hint selects against output and returns
// the highest-scoring parse. Output is used as given; clean it with
// CleanOutput first if it is a session transcript.
//
// A template has to score above zero to win, and a later template has to
// score strictly higher to replace an earlier one, so ties go to the lower
// rowid -- both as in the Python engine.
func (e *Engine) FindBest(output, hint string) Match {
	cands := e.Filter(hint)
	best := Match{Tried: len(cands)}
	for _, t := range cands {
		if t.CompileErr != "" {
			continue
		}
		recs, err := parse(t.Content, output)
		if err != nil {
			continue
		}
		score := Score(recs, t.Name).Total
		if score > best.Score {
			best.Score = score
			best.Template = t.Name
			best.Header = t.Header
			best.Records = recs
		}
	}
	return best
}

// parse compiles src and runs it over output. A fresh compile per call is the
// concurrency boundary described in the package comment.
func parse(src, output string) ([]Record, error) {
	var fsm gotextfsm.TextFSM
	if err := fsm.ParseString(src); err != nil {
		return nil, err
	}
	var p gotextfsm.ParserOutput
	if err := p.ParseTextString(output, fsm, true); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(p.Dict))
	for _, d := range p.Dict {
		r := make(Record, len(d))
		for k, v := range d {
			r[k] = v
		}
		out = append(out, r)
	}
	return out, nil
}

func filterTerms(hint string) []string {
	var terms []string
	for _, term := range strings.Split(strings.ReplaceAll(hint, "-", "_"), "_") {
		if len(term) > 2 {
			terms = append(terms, asciiLower(term))
		}
	}
	return terms
}

// matchesTerms is `cli_command LIKE %term%` for every term. SQLite's LIKE
// folds ASCII case only, and the terms cannot contain '_' (they were split on
// it) -- the one LIKE wildcard that could otherwise change the match. A '%'
// in a hint is not something a platform or command name contains.
func matchesTerms(name string, terms []string) bool {
	lower := asciiLower(name)
	for _, term := range terms {
		if !strings.Contains(lower, term) {
			return false
		}
	}
	return true
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// headerOf reads the Value names from template source, in order. TextFSM's
// grammar: "Value [Option[,Option...]] NAME (regex)".
func headerOf(src string) []string {
	options := map[string]bool{"Filldown": true, "Key": true, "Required": true, "List": true, "Fillup": true}
	var out []string
	for _, line := range strings.Split(src, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != "Value" {
			if len(f) == 0 || f[0] == "Value" || strings.HasPrefix(f[0], "#") {
				continue
			}
			// The Value block ends at the first line that is neither a
			// Value, a comment nor blank.
			break
		}
		name := f[1]
		if len(f) >= 4 {
			allOpts := true
			for _, o := range strings.Split(f[1], ",") {
				if !options[o] {
					allOpts = false
					break
				}
			}
			if allOpts {
				name = f[2]
			}
		}
		out = append(out, name)
	}
	return out
}
