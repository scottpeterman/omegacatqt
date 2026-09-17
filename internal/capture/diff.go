// internal/capture/diff.go
//
// Line diffs between two stored versions of a static capture.
//
// # Which types
//
// Only captures whose content changes when the device's state does:
// running-config, startup-config, inventory. An ARP or MAC table differs
// between any two captures because ages tick and entries come and go, and a
// line diff of one is noise; the useful comparison there is of parsed records,
// which needs parses you trust first. Diffable says which types qualify, and
// the store refuses the rest rather than returning a diff nobody should read.
//
// # How
//
// Myers' O((N+M)D) algorithm over interned lines, after trimming the common
// prefix and suffix -- a config change is usually a few lines in thousands,
// and trimming makes D, not N, the cost. D is capped: two versions that
// differ in more than MaxDiffEdits lines are reported as too different for a
// line diff rather than walked, because the trace Myers keeps grows with D
// squared and a replaced config is not something a line diff explains anyway.
package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// MaxDiffEdits bounds the edit distance a diff will compute.
const MaxDiffEdits = 4000

// ErrTooDifferent is returned when two versions differ by more than
// MaxDiffEdits lines.
var ErrTooDifferent = errors.New("the versions differ in too many lines for a line diff")

var diffableTypes = map[string]bool{
	"running-config": true,
	"startup-config": true,
	"inventory":      true,
}

// Diffable reports whether a capture type gets a line diff.
func Diffable(typ string) bool { return diffableTypes[typ] }

// DiffLine is one line of a hunk. Op is '=' (context), '-' (only in the older
// version) or '+' (only in the newer). A and B are 1-based line numbers in the
// older and newer versions, 0 where the line is absent.
type DiffLine struct {
	Op   string `json:"op"`
	A    int    `json:"a,omitempty"`
	B    int    `json:"b,omitempty"`
	Text string `json:"text"`
	// Ignored marks a changed line an ignore rule matches: shown, not
	// counted, and never the reason a hunk exists.
	Ignored bool `json:"ignored,omitempty"`
}

// Hunk is a run of changes with its context.
type Hunk struct {
	AStart int        `json:"a_start"`
	ALen   int        `json:"a_len"`
	BStart int        `json:"b_start"`
	BLen   int        `json:"b_len"`
	Lines  []DiffLine `json:"lines"`
}

// Diff is a comparison of two versions.
type Diff struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Identical bool   `json:"identical"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	// Ignored counts changed lines an ignore rule matched. With Added and
	// Removed both zero and Ignored above zero, the versions differ only
	// in such lines.
	Ignored  int    `json:"ignored"`
	Platform string `json:"platform,omitempty"`
	Hunks    []Hunk `json:"hunks"`
}

func splitLines(b []byte) []string {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\n"))
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte("\n"))
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = string(bytes.TrimSuffix(p, []byte("\r")))
	}
	return out
}

// DiffText compares two texts line by line with context lines around each
// change (negative means 3).
func DiffText(older, newer []byte, context int) (Diff, error) {
	return DiffTextIgnoring(older, newer, context, nil)
}

// DiffTextIgnoring is DiffText with changed lines matching any of ignore
// marked Ignored, left out of the counts, and not opening hunks of their own.
func DiffTextIgnoring(older, newer []byte, context int, ignore []*regexp.Regexp) (Diff, error) {
	if context < 0 {
		context = 3
	}
	a, b := splitLines(older), splitLines(newer)
	ops, err := editScript(a, b)
	if err != nil {
		return Diff{}, err
	}
	d := Diff{Hunks: []Hunk{}}
	for i := range ops {
		o := &ops[i]
		if o.Op == "=" {
			continue
		}
		for _, re := range ignore {
			if re.MatchString(o.Text) {
				o.Ignored = true
				break
			}
		}
		switch {
		case o.Ignored:
			d.Ignored++
		case o.Op == "+":
			d.Added++
		default:
			d.Removed++
		}
	}
	d.Identical = d.Added == 0 && d.Removed == 0 && d.Ignored == 0
	if d.Added == 0 && d.Removed == 0 {
		return d, nil
	}
	// A change that counts; ignored ones are context as far as hunks go.
	counts := func(o DiffLine) bool { return o.Op != "=" && !o.Ignored }

	// Group changes into hunks: a change starts or extends a hunk, and a
	// hunk ends once more than 2*context unchanged lines separate it from
	// the next change.
	for i := 0; i < len(ops); {
		if !counts(ops[i]) {
			i++
			continue
		}
		start := i - context
		if start < 0 {
			start = 0
		}
		end := i
		for j := i; j < len(ops); j++ {
			if counts(ops[j]) {
				end = j
				continue
			}
			if j-end > 2*context {
				break
			}
		}
		stop := end + context + 1
		if stop > len(ops) {
			stop = len(ops)
		}
		h := Hunk{Lines: append([]DiffLine(nil), ops[start:stop]...)}
		for _, l := range h.Lines {
			if l.A > 0 {
				if h.AStart == 0 {
					h.AStart = l.A
				}
				h.ALen++
			}
			if l.B > 0 {
				if h.BStart == 0 {
					h.BStart = l.B
				}
				h.BLen++
			}
		}
		d.Hunks = append(d.Hunks, h)
		i = stop
	}
	return d, nil
}

// editScript is the line-level edit script from a to b, as DiffLines.
func editScript(a, b []string) ([]DiffLine, error) {
	// Common prefix and suffix cost nothing to find and usually are most of
	// the file.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	midA, midB := a[pre:len(a)-suf], b[pre:len(b)-suf]

	var out []DiffLine
	for i := 0; i < pre; i++ {
		out = append(out, DiffLine{Op: "=", A: i + 1, B: i + 1, Text: a[i]})
	}
	mid, err := myers(midA, midB)
	if err != nil {
		return nil, err
	}
	for _, l := range mid {
		if l.A > 0 {
			l.A += pre
		}
		if l.B > 0 {
			l.B += pre
		}
		out = append(out, l)
	}
	for i := 0; i < suf; i++ {
		ai, bi := len(a)-suf+i, len(b)-suf+i
		out = append(out, DiffLine{Op: "=", A: ai + 1, B: bi + 1, Text: a[ai]})
	}
	return out, nil
}

// myers is the shortest edit script between a and b.
func myers(a, b []string) ([]DiffLine, error) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, nil
	}
	// Intern lines so the inner loop compares ints.
	ids := map[string]int{}
	intern := func(s []string) []int {
		out := make([]int, len(s))
		for i, x := range s {
			id, ok := ids[x]
			if !ok {
				id = len(ids)
				ids[x] = id
			}
			out[i] = id
		}
		return out
	}
	ia, ib := intern(a), intern(b)

	max := n + m
	offset := max
	v := make([]int, 2*max+2)
	var trace [][]int
	found := -1
	for d := 0; d <= max; d++ {
		if d > MaxDiffEdits {
			return nil, fmt.Errorf("%w (more than %d)", ErrTooDifferent, MaxDiffEdits)
		}
		// Keep only the diagonals this step can reach.
		snap := make([]int, 2*d+1)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && ia[x] == ib[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = d
			}
		}
		for k := -d; k <= d; k++ {
			snap[k+d] = v[offset+k]
		}
		trace = append(trace, snap)
		if found >= 0 {
			break
		}
	}

	// Walk the trace back from (n, m).
	var rev []DiffLine
	x, y := n, m
	for d := found; d > 0; d-- {
		prev := trace[d-1]
		at := func(k int) int { return prev[k+(d-1)] }
		k := x - y
		var pk int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := at(pk)
		py := px - pk
		for x > px && y > py {
			x--
			y--
			rev = append(rev, DiffLine{Op: "=", A: x + 1, B: y + 1, Text: a[x]})
		}
		if x == px {
			y--
			rev = append(rev, DiffLine{Op: "+", B: y + 1, Text: b[y]})
		} else {
			x--
			rev = append(rev, DiffLine{Op: "-", A: x + 1, Text: a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, DiffLine{Op: "=", A: x + 1, B: y + 1, Text: a[x]})
	}
	out := make([]DiffLine, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out, nil
}

// Diff compares two stored versions of a capture, older first.
func (s *FileStore) Diff(canonical, typ, olderFile, newerFile string, context int) (Diff, error) {
	return s.DiffWith(canonical, typ, olderFile, newerFile, context, nil)
}

// DiffWith is Diff with ignore rules chosen by the device's stored platform.
func (s *FileStore) DiffWith(canonical, typ, olderFile, newerFile string, context int,
	rulesFor func(platform string) []*regexp.Regexp) (Diff, error) {
	if !Diffable(typ) {
		return Diff{}, fmt.Errorf("%s is not diffed line by line; only running-config, startup-config and inventory are", typ)
	}
	older, err := s.Read(canonical, typ, olderFile)
	if err != nil {
		return Diff{}, err
	}
	newer, err := s.Read(canonical, typ, newerFile)
	if err != nil {
		return Diff{}, err
	}
	platform := s.platformOf(canonical)
	var ignore []*regexp.Regexp
	if rulesFor != nil {
		ignore = rulesFor(platform)
	}
	d, err := DiffTextIgnoring(older, newer, context, ignore)
	if err != nil {
		return Diff{}, err
	}
	d.From, d.To, d.Platform = olderFile, newerFile, platform
	return d, nil
}

// platformOf is the platform device.json records, or "".
func (s *FileStore) platformOf(canonical string) string {
	dir, err := s.deviceDir(canonical)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "device.json"))
	if err != nil {
		return ""
	}
	var info DeviceInfo
	if json.Unmarshal(data, &info) != nil {
		return ""
	}
	return info.Platform
}
