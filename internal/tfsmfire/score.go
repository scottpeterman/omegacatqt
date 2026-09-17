// internal/tfsmfire/score.go
//
// The scoring function, transcribed from tfsm_fire.py's _score_parts. The
// arithmetic is written in the same order as the Python so float64 results are
// bit-identical: the parity test compares scores, not just winners, and a
// reordered sum is a different number in the last place.
//
// Every product that reaches a sum is wrapped in an explicit float64() -- the
// ones written "a + b*c", and the population and consistency products too,
// which the compiler otherwise carries into the final total and fuses there. The Go spec lets
// the compiler fuse x*y+z into one FMA instruction, which rounds once instead
// of twice; arm64 does it, amd64 does not by default, and Python never does.
// Unwrapped, the same source scored 84.72222222222221 on Apple Silicon against
// Python's 84.72222222222223 -- and an explicit conversion is what the spec
// says prevents the fusion.
package tfsmfire

import "strings"

// Breakdown is the four components and their sum. The maximum is 220 (90 + 90
// + 25 + 15), whatever the Python docstring's "0-100" says; only relative
// order between candidates, and netlapse's minimum-score threshold, use it.
type Breakdown struct {
	Records     float64 `json:"records"`
	Fields      float64 `json:"fields"`
	Population  float64 `json:"population"`
	Consistency float64 `json:"consistency"`
	Total       float64 `json:"total"`
}

// Score rates one template's parse. name is the template's name, used only to
// spot version commands, which are expected to produce exactly one record.
func Score(recs []Record, name string) Breakdown {
	if len(recs) == 0 {
		return Breakdown{}
	}
	numRecords := len(recs)
	numFields := len(recs[0])
	isVersion := strings.Contains(strings.ToLower(name), "version")

	var recordScore float64
	switch {
	case isVersion:
		if numRecords == 1 {
			recordScore = 90.0
		} else {
			recordScore = float64(max(0, 15-(numRecords-1)*5))
		}
	case numRecords >= 10:
		recordScore = 90.0
	case numRecords >= 3:
		recordScore = 20.0 + float64(float64(numRecords-3)*(10.0/7.0))
	default:
		recordScore = float64(numRecords) * 10.0
	}

	var fieldScore float64
	switch {
	case numFields >= 10:
		fieldScore = 90.0
	case numFields >= 6:
		fieldScore = 20.0 + float64(float64(numFields-6)*2.5)
	case numFields >= 3:
		fieldScore = 10.0 + float64(float64(numFields-3)*(10.0/3.0))
	default:
		fieldScore = float64(numFields) * 5.0
	}

	totalCells := numRecords * numFields
	populated := 0
	for _, r := range recs {
		for _, v := range r {
			if populatedValue(v) {
				populated++
			}
		}
	}
	populationRate := 0.0
	if totalCells > 0 {
		populationRate = float64(populated) / float64(totalCells)
	}
	populationScore := float64(populationRate * 25.0)

	var consistencyScore float64
	if numRecords > 1 {
		// Keyed on the FIRST record's fields, as the Python is: a key only
		// a later record has is counted by neither side of the ratio.
		fill := make(map[string]int, numFields)
		for k := range recs[0] {
			fill[k] = 0
		}
		for _, r := range recs {
			for k, v := range r {
				if _, tracked := fill[k]; tracked && populatedValue(v) {
					fill[k]++
				}
			}
		}
		consistent := 0
		for _, c := range fill {
			if c == 0 || c == numRecords {
				consistent++
			}
		}
		consistencyRate := 0.0
		if numFields > 0 {
			consistencyRate = float64(consistent) / float64(numFields)
		}
		consistencyScore = float64(consistencyRate * 15.0)
	} else {
		consistencyScore = 15.0
	}

	return Breakdown{
		Records:     recordScore,
		Fields:      fieldScore,
		Population:  populationScore,
		Consistency: consistencyScore,
		Total:       recordScore + fieldScore + populationScore + consistencyScore,
	}
}

// populatedValue is Python's `value is not None and str(value).strip()`.
//
// For a List Value that is always true: str([]) is "[]", which is not blank,
// so an empty list counts as populated. Reproduced on purpose -- it moves the
// population and consistency scores of every template with a List Value, and
// matching Python's choice of template depends on it.
func populatedValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	default:
		return true
	}
}
