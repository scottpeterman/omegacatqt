// internal/tfsmlab/lab.go
//
// The Template Lab's engine: netlapse's ParseEngine (parser/engine.py) and the
// template routes of its admin API (web/api/admin.py), over tfsmfire.
//
// tfsmfire is tfsm_fire.py -- one template, or the best of a hint's
// candidates. This is the layer netlapse puts on top of it, and the Lab's
// answers have to be netlapse's answers, because the same database is edited
// in both:
//
//	parse            clean, filter on platform + command, accept at 15
//	vendor fallback  clean, filter on the vendor alone, accept at 15
//	test template    optionally clean, then tfsm_fire's test_template
//
// # Not OmegaCat's capture hint
//
// A capture parses with its spec's TemplateHint ("cisco_ios_show_arp"); the Lab
// sweeps with netlapse's filter built from the command ("cisco_ios_show_ip_arp").
// They differ on purpose. The Lab answers "which template wins this command's
// output", for any command, and the capture hints exist only for the ARP and
// MAC types. Both feed the same tfsmfire.Filter, so a template that wins in
// the Lab under the capture's hint wins the capture.
package tfsmlab

import (
	"sort"
	"strconv"
	"strings"

	"github.com/scottpeterman/omegacatqt/internal/tfsmfire"
)

// MinScore is ParseEngine's DEFAULT_MIN_SCORE, the same threshold as
// capture.MinParseScore (lab_test.go holds the two together).
const MinScore = 15.0

// Result is ParseEngine's ParseResult.
type Result struct {
	Success     bool              `json:"success"`
	Template    string            `json:"template,omitempty"`
	Header      []string          `json:"header"`
	Records     []tfsmfire.Record `json:"records"`
	RecordCount int               `json:"record_count"`
	Score       float64           `json:"score"`
	Error       string            `json:"error,omitempty"`
	// Filter is the hint the parse ran with, so the Lab can show it.
	Filter string `json:"filter"`
	Tried  int    `json:"tried"`
}

// Candidate is one template a filter selects.
type Candidate struct {
	RowID      int64  `json:"rowid"`
	Name       string `json:"cli_command"`
	CompileErr string `json:"compile_error,omitempty"`
}

// Sweep is admin.py's POST /templates/test: the primary parse, the vendor
// fallback when the primary fails (present only when it succeeded), and the
// primary filter's candidates.
type Sweep struct {
	Primary    Result      `json:"primary"`
	Fallback   *Result     `json:"fallback,omitempty"`
	Candidates []Candidate `json:"candidates"`
}

// BuildFilter is ParseEngine._build_filter: platform and the command, spaces
// and hyphens to underscores, joined with '_', empty parts left out.
//
//	("arista_eos", "show ip arp")         -> arista_eos_show_ip_arp
//	("cisco_ios", "show running-config")  -> cisco_ios_show_running_config
func BuildFilter(platform, command string) string {
	cmd := strings.ReplaceAll(strings.ReplaceAll(pyStrip(command), " ", "_"), "-", "_")
	var parts []string
	for _, p := range []string{platform, cmd} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "_")
}

// Vendor is parse_vendor_fallback's vendor: the platform up to its first '_'.
func Vendor(platform string) string {
	if platform == "" {
		return ""
	}
	return strings.SplitN(platform, "_", 2)[0]
}

// Parse is ParseEngine.parse.
func Parse(eng *tfsmfire.Engine, raw, platform, command string) Result {
	return parseWith(eng, raw, BuildFilter(platform, command))
}

// VendorFallback is ParseEngine.parse_vendor_fallback.
func VendorFallback(eng *tfsmfire.Engine, raw, platform string) Result {
	vendor := Vendor(platform)
	if pyStrip(raw) == "" {
		return empty("empty output", vendor)
	}
	if vendor == "" {
		return empty("no vendor", vendor)
	}
	return parseWith(eng, raw, vendor)
}

func parseWith(eng *tfsmfire.Engine, raw, filter string) Result {
	if pyStrip(raw) == "" {
		return empty("empty output", filter)
	}
	m := eng.FindBest(tfsmfire.CleanOutput(raw), filter)
	r := empty("", filter)
	r.Template = m.Template
	r.Score = round1(m.Score)
	r.Tried = m.Tried
	// Python: score >= min_score and parsed_data. The comparison is on the
	// unrounded score; only the reported one is rounded.
	if m.Score >= MinScore && len(m.Records) > 0 {
		r.Success = true
		r.Header = m.Header
		r.Records = m.Records
		r.RecordCount = len(m.Records)
	}
	return r
}

func empty(errMsg, filter string) Result {
	return Result{Header: []string{}, Records: []tfsmfire.Record{}, Error: errMsg, Filter: filter}
}

// RunSweep is admin.py's test_parse.
func RunSweep(eng *tfsmfire.Engine, raw, platform, command string) Sweep {
	s := Sweep{Primary: Parse(eng, raw, platform, command)}
	if !s.Primary.Success {
		if fb := VendorFallback(eng, raw, platform); fb.Success {
			s.Fallback = &fb
		}
	}
	s.Candidates = Candidates(eng, BuildFilter(platform, command))
	return s
}

// Candidates is admin.py's _get_candidate_templates, sorted by name as its
// query is. It asks the engine's own filter rather than repeating the LIKE
// query, so the list is exactly what the sweep tried. (admin.py builds its
// filter string slightly differently -- it drops '|' from the command where
// _build_filter keeps it -- but a lone '|' is a one-character term and the
// filter discards it either way.)
func Candidates(eng *tfsmfire.Engine, filter string) []Candidate {
	out := []Candidate{}
	for _, t := range eng.Filter(filter) {
		out = append(out, Candidate{RowID: t.RowID, Name: t.Name, CompileErr: t.CompileErr})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Test is ParseEngine.test_template: one ad-hoc template, the output cleaned
// first when clean is set. command is used only for the score's
// version-command detection.
func Test(src, raw, command string, clean bool) tfsmfire.TestResult {
	text := raw
	if clean {
		text = tfsmfire.CleanOutput(raw)
	}
	return tfsmfire.TestTemplate(src, text, command)
}

// pyStrip is Python's str.strip().
func pyStrip(s string) string { return strings.TrimSpace(s) }

// round1 is Python's round(x, 1), as tfsmfire rounds: on the exact binary
// value, halves to even.
func round1(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 1, 64), 64)
	return r
}
