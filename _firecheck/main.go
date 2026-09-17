package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/sirikothe/gotextfsm"
)

type row struct {
	RowID  int    `json:"rowid"`
	Name   string `json:"name"`
	Sample string `json:"sample"`
	Src    string `json:"src"`
}
type pyRec struct {
	Compile *string          `json:"compile"`
	Records []map[string]any `json:"records"`
	Error   *string          `json:"error"`
}

func norm(v any) any {
	switch x := v.(type) {
	case []any:
		o := []string{}
		for _, e := range x {
			o = append(o, fmt.Sprint(e))
		}
		return o
	case []string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func main() {
	var rows []row
	b, _ := os.ReadFile("/tmp/fire/rows.json")
	json.Unmarshal(b, &rows)
	var py map[string]pyRec
	b, _ = os.ReadFile("/tmp/fire/py_parse.json")
	json.Unmarshal(b, &py)

	compileFail := map[string]string{}
	var same, diff, goErr int
	var diffs []string
	byName := map[string]string{}
	for _, r := range rows {
		fsm := gotextfsm.TextFSM{}
		if err := fsm.ParseString(r.Src); err != nil {
			compileFail[r.Name] = err.Error()
			continue
		}
		p := gotextfsm.ParserOutput{}
		if err := p.ParseTextString(r.Sample, fsm, true); err != nil {
			goErr++
			diffs = append(diffs, "go runtime error: "+r.Name+": "+err.Error())
			continue
		}
		want := py[r.Name].Records
		ok := len(want) == len(p.Dict)
		if ok {
			for i := range want {
				g, w := map[string]any{}, map[string]any{}
				for k, v := range p.Dict[i] {
					g[k] = norm(v)
				}
				for k, v := range want[i] {
					w[k] = norm(v)
				}
				if !reflect.DeepEqual(g, w) {
					ok = false
					if len(diffs) < 12 {
						diffs = append(diffs, fmt.Sprintf("%s rec %d: go=%v py=%v", r.Name, i, g, w))
					}
					break
				}
			}
		} else if len(diffs) < 12 {
			diffs = append(diffs, fmt.Sprintf("%s: go %d records, py %d", r.Name, len(p.Dict), len(want)))
		}
		if ok {
			same++
		} else {
			diff++
			byName[r.Name] = "diff"
		}
	}
	fmt.Printf("templates %d: go compile failures %d; parsed identically to python %d, differ %d, go runtime errors %d\n",
		len(rows), len(compileFail), same, diff, goErr)
	reasons := map[string][]string{}
	for n, e := range compileFail {
		k := e
		switch {
		case strings.Contains(e, "(?<") || strings.Contains(e, "(?=") || strings.Contains(e, "(?!"):
			k = "lookaround (RE2 has none)"
		case strings.Contains(e, "\\1") || strings.Contains(e, "\\2"):
			k = "backreference"
		default:
			if len(k) > 90 {
				k = k[:90]
			}
		}
		reasons[k] = append(reasons[k], n)
	}
	keys := []string{}
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(reasons[keys[i]]) > len(reasons[keys[j]]) })
	for _, k := range keys {
		fmt.Printf("  %3d  %s  e.g. %s\n", len(reasons[k]), k, reasons[k][0])
	}
	for _, d := range diffs {
		if len(d) > 300 {
			d = d[:300]
		}
		fmt.Println("  ", d)
	}
}
