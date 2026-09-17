package tfsmfire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// knownIncompatible are shipped templates that Go's regexp engine cannot run,
// with why. They are listed, not hidden: a template tool shows them as not
// runnable here, and each is a rewrite waiting to be done and proven (see
// scripts/tfsm_re2_rewrite.py). This test fails if one starts compiling --
// remove it from the list -- or if a template not on the list stops.
var knownIncompatible = map[string]string{
	"cisco_asa_show_access-list":                            "negative lookahead in DST_ICMP_TYPE",
	"cisco_ios_show_ip_access-lists":                        "lookbehind on 'range' for port values",
	"cisco_ios_show_ip_bgp":                                 "fixed-width column lookaheads",
	"cisco_ios_show_object-group":                           "lookbehind on 'range' for port values",
	"cisco_ios_show_running-config_partition_access-list":   "lookbehind on 'range' for port values",
	"cisco_ios_traceroute":                                  "lookahead in List RTT_RESPONSE",
	"fortinet_execute_traceroute":                           "negative lookahead in ADDRESS",
	"fortinet_get_router_info_ospf_status":                  "negative lookahead in a rule",
	"juniper_junos_show_version":                            "named groups inside a List Value (Python yields a list of dicts)",
	"mikrotik_routeros_ip_neighbor_print_detail":            "lookahead in quoted values",
	"mikrotik_routeros_ip_route_print_detail":               "lookahead in GATEWAY_STATUS",
	"mikrotik_routeros_ip_route_print_terse_without-paging": "lookaheads in key=value values",
	"paloalto_panos_show_running_nat-policy":                "a Value regex gotextfsm rejects as not wrapped in one group",
}

// Every template that compiles in Go must parse its own sample to exactly the
// records Python TextFSM produced (testdata/samples.json, from
// scripts/tfsm_samples.py). This is the parse-level half of parity; the
// selection half is parity_test.go.
func TestSamplesParseLikePython(t *testing.T) {
	if raceEnabled {
		// Thousands of parses over the whole corpus take minutes under the
		// race detector and exercise no concurrency;
		// TestFindBestIsSafeConcurrently is the race coverage.
		t.Skip("corpus parity runs without -race")
	}
	var golden map[string]struct {
		Records int    `json:"records"`
		SHA256  string `json:"sha256"`
		Error   string `json:"error"`
	}
	b, err := os.ReadFile(filepath.Join("testdata", "samples.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}

	e := seedEngine(t)
	var identical, differ int
	var notCompiling []string
	for _, tpl := range e.Templates() {
		if tpl.CompileErr != "" {
			notCompiling = append(notCompiling, tpl.Name)
			continue
		}
		g, ok := golden[tpl.Name]
		if !ok {
			continue // no stored sample
		}
		recs, err := parse(tpl.Content, tpl.Sample)
		if g.Error != "" {
			if err == nil {
				t.Errorf("%s: Python raised %q; Go parsed %d records", tpl.Name, g.Error, len(recs))
			}
			continue
		}
		if err != nil {
			if _, known := knownRecordDifferences[tpl.Name]; !known {
				t.Errorf("%s: Go failed where Python parsed %d records: %v", tpl.Name, g.Records, err)
			}
			differ++
			continue
		}
		if sum := canonicalDigest(t, recs); sum != g.SHA256 {
			if _, known := knownRecordDifferences[tpl.Name]; !known {
				t.Errorf("%s: Go records (%d) differ from Python's (%d)", tpl.Name, len(recs), g.Records)
			}
			differ++
			continue
		}
		identical++
	}

	sort.Strings(notCompiling)
	for _, n := range notCompiling {
		if _, ok := knownIncompatible[n]; !ok {
			t.Errorf("%s no longer compiles in Go and is not a known incompatibility", n)
		}
	}
	all := map[string]bool{}
	for _, n := range notCompiling {
		all[n] = true
	}
	for n := range knownIncompatible {
		if tpl, ok := e.Template(n); ok && tpl.CompileErr == "" && !all[n] {
			t.Errorf("%s now compiles in Go; remove it from knownIncompatible", n)
		}
	}
	t.Logf("%d identical to Python, %d differ (%d known), %d do not compile in Go",
		identical, differ, len(knownRecordDifferences), len(notCompiling))
}

// knownRecordDifferences are templates that compile in Go and parse their
// sample to different records than Python does -- engine differences in
// gotextfsm, not template bugs. Filled from the first run of this test.
var knownRecordDifferences = map[string]string{
	// Inside a List value, Python keeps None for an optional group that did
	// not match; gotextfsm stores "" or leaves the element out. The score is
	// unaffected (a list counts as populated either way), so selection is
	// too, but the records are not byte-identical.
	"brocade_fastiron_show_version":              "List element None vs \"\"",
	"cisco_asa_show_failover":                    "List element None vs dropped",
	"cisco_asa_show_running-config_tunnel-group": "List element None vs \"\"",
	"cisco_asa_show_vpn-sessiondb":               "List element None vs \"\"",
	// gotextfsm raises the template's Error state on a line Python TextFSM
	// matches -- a real engine divergence; FindBest skips the template here.
	"mikrotik_routeros_ip_dhcp-server_lease_print_without-paging": "State Error in Go only",
}

// canonicalDigest reproduces scripts/tfsm_samples.py's digest: JSON with keys
// sorted, no whitespace, no HTML escaping, SHA-256.
func canonicalDigest(t *testing.T, recs []Record) string {
	t.Helper()
	if recs == nil {
		recs = []Record{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(recs); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])
}
