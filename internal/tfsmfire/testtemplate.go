// internal/tfsmfire/testtemplate.go
//
// TestTemplate, ported from tfsm_fire.py: run ONE caller-supplied template --
// not the database -- and report everything, failures included. This is the
// call behind a template tool's "Test Template" button.
package tfsmfire

import (
	"regexp"
	"strconv"
	"strings"
)

// TestResult is one template run against one output.
type TestResult struct {
	Compiled    bool      `json:"compiled"`
	Success     bool      `json:"success"`
	Error       string    `json:"error,omitempty"`
	ErrorType   string    `json:"error_type,omitempty"` // "compile" or "runtime"
	RuleLine    int       `json:"rule_line,omitempty"`
	InputLine   string    `json:"input_line,omitempty"`
	Header      []string  `json:"header"`
	Records     []Record  `json:"records"`
	RecordCount int       `json:"record_count"`
	FieldCount  int       `json:"field_count"`
	Score       float64   `json:"score"`
	Breakdown   Breakdown `json:"breakdown"`
}

var (
	ruleLineRe  = regexp.MustCompile(`Rule Line:\s*(\d+)`)
	inputLineRe = regexp.MustCompile(`Input Line:\s*(.*)`)
)

// TestTemplate compiles src and runs it over output. name is used only for
// version-command detection in the score. Output is used as given.
//
// Score and breakdown are rounded to one decimal place, as the Python rounds
// them for display. FindBest's scores are not rounded.
func TestTemplate(src, output, name string) TestResult {
	res := TestResult{Header: []string{}, Records: []Record{}}

	if err := compileCheck(src); err != nil {
		res.Error = err.Error()
		res.ErrorType = "compile"
		return res
	}
	res.Compiled = true
	res.Header = headerOf(src)

	recs, err := parse(src, output)
	if err != nil {
		msg := err.Error()
		res.Error = msg
		res.ErrorType = "runtime"
		if m := ruleLineRe.FindStringSubmatch(msg); m != nil {
			res.RuleLine, _ = strconv.Atoi(m[1])
		}
		if m := inputLineRe.FindStringSubmatch(msg); m != nil {
			res.InputLine = strings.TrimSpace(m[1])
		}
		return res
	}

	b := Score(recs, name)
	res.Success = true
	res.Records = recs
	res.RecordCount = len(recs)
	res.FieldCount = len(res.Header)
	res.Score = round1(b.Total)
	res.Breakdown = Breakdown{
		Records: round1(b.Records), Fields: round1(b.Fields),
		Population: round1(b.Population), Consistency: round1(b.Consistency),
		Total: round1(b.Total),
	}
	return res
}
