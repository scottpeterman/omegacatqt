package tfsmlab

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/tfsmfire"
)

// A session transcript from a lab EOS switch: echo, output, trailing prompt.
const eosARPSession = `lab-leaf-1#show ip arp
Address         Age (sec)  Hardware Addr   Interface
172.16.10.1       0:00:12  001c.7300.0001  Vlan10, Ethernet1
172.16.10.20      0:01:40  001c.7300.0020  Vlan10, Ethernet7
172.16.20.1       0:00:03  001c.7300.0101  Vlan20, Ethernet2
172.16.20.44      0:02:55  001c.7300.0144  Vlan20, Ethernet9
lab-leaf-1#`

const arpTemplate = `Value ADDRESS (\d+\.\d+\.\d+\.\d+)
Value AGE (\S+)
Value MAC (\S+)
Value INTERFACE (.+?)

Start
  ^Address\s+Age
  ^${ADDRESS}\s+${AGE}\s+${MAC}\s+${INTERFACE}\s*$$ -> Record
`

// seedCopy writes the shipped database to a temporary file. Never the user's.
func seedCopy(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tfsm_templates.db")
	if err := os.WriteFile(p, tfsmfire.Seed(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	engOnce sync.Once
	engSeed *tfsmfire.Engine
	engErr  error
)

// seedEngine is one read-only engine over the shipped database for the
// parse tests: opening one compiles every template.
func seedEngine(t *testing.T) *tfsmfire.Engine {
	t.Helper()
	engOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tfsmlab")
		if err != nil {
			engErr = err
			return
		}
		p := filepath.Join(dir, "seed.db")
		if engErr = os.WriteFile(p, tfsmfire.Seed(), 0o600); engErr == nil {
			engSeed, engErr = tfsmfire.Open(p)
		}
	})
	if engErr != nil {
		t.Fatal(engErr)
	}
	return engSeed
}

func TestMinScoreIsTheCaptureThreshold(t *testing.T) {
	if MinScore != capture.MinParseScore {
		t.Fatalf("MinScore %v, capture.MinParseScore %v: the Lab would accept parses a capture rejects", MinScore, capture.MinParseScore)
	}
}

func TestBuildFilter(t *testing.T) {
	// The examples in ParseEngine._build_filter's docstring.
	for _, c := range []struct{ platform, command, want string }{
		{"arista_eos", "show ip arp", "arista_eos_show_ip_arp"},
		{"cisco_ios", "show ip bgp summary", "cisco_ios_show_ip_bgp_summary"},
		{"cisco_ios", "show running-config", "cisco_ios_show_running_config"},
		{"arista_eos", "", "arista_eos"},
		{"", "show version", "show_version"},
		{"juniper_junos", "  show arp no-resolve  ", "juniper_junos_show_arp_no_resolve"},
	} {
		if got := BuildFilter(c.platform, c.command); got != c.want {
			t.Errorf("BuildFilter(%q, %q) = %q, want %q", c.platform, c.command, got, c.want)
		}
	}
	for in, want := range map[string]string{"juniper_junos": "juniper", "cisco": "cisco", "": ""} {
		if got := Vendor(in); got != want {
			t.Errorf("Vendor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseCleansAndAcceptsAtThreshold(t *testing.T) {
	eng := seedEngine(t)
	r := Parse(eng, eosARPSession, "arista_eos", "show ip arp")
	if !r.Success || r.Template != "arista_eos_show_ip_arp" || r.RecordCount != 4 {
		t.Fatalf("primary parse: %+v", r)
	}
	if r.Filter != "arista_eos_show_ip_arp" {
		t.Errorf("filter %q", r.Filter)
	}
	// The same output uncleaned carries the echo and prompt lines; cleaning
	// is part of parse(), not the caller's job.
	if r.Score < MinScore {
		t.Errorf("score %v", r.Score)
	}
	if e := Parse(eng, "  \n ", "arista_eos", "show ip arp"); e.Success || e.Error != "empty output" {
		t.Errorf("empty output: %+v", e)
	}
}

func TestSweepFallsBackToTheVendor(t *testing.T) {
	eng := seedEngine(t)
	// A command no arista template is named for: the primary filter selects
	// nothing, so the vendor fallback has to find the ARP template.
	s := RunSweep(eng, eosARPSession, "arista_eos", "show lab neighbours cache")
	if s.Primary.Success || s.Primary.Tried != 0 || len(s.Candidates) != 0 {
		t.Fatalf("primary should select nothing: %+v, %d candidates", s.Primary, len(s.Candidates))
	}
	if s.Fallback == nil || !s.Fallback.Success || s.Fallback.Filter != "arista" {
		t.Fatalf("fallback: %+v", s.Fallback)
	}
	if !strings.HasPrefix(s.Fallback.Template, "arista_eos_") {
		t.Errorf("fallback chose %q", s.Fallback.Template)
	}

	// A primary that succeeds runs no fallback, and lists its candidates by name.
	ok := RunSweep(eng, eosARPSession, "arista_eos", "show ip arp")
	if !ok.Primary.Success || ok.Fallback != nil || len(ok.Candidates) == 0 {
		t.Fatalf("primary sweep: %+v, fallback %v, %d candidates", ok.Primary, ok.Fallback, len(ok.Candidates))
	}
	for i := 1; i < len(ok.Candidates); i++ {
		if ok.Candidates[i-1].Name > ok.Candidates[i].Name {
			t.Fatalf("candidates not sorted: %q before %q", ok.Candidates[i-1].Name, ok.Candidates[i].Name)
		}
	}

	if fb := VendorFallback(eng, eosARPSession, ""); fb.Success || fb.Error != "no vendor" {
		t.Errorf("no platform: %+v", fb)
	}
}

func TestTestCleanIsOptional(t *testing.T) {
	cleaned := Test(arpTemplate, eosARPSession, "arista_eos_show_ip_arp", true)
	if !cleaned.Success || cleaned.RecordCount != 4 {
		t.Fatalf("cleaned: %+v", cleaned)
	}
	// Uncleaned, the prompt line "lab-leaf-1#" does not match the record rule
	// and is simply skipped -- so the difference has to come from a strict
	// template. With "-> Error" on unmatched lines, the echo line is fatal.
	strict := arpTemplate + "  ^.* -> Error\n"
	raw := Test(strict, eosARPSession, "arista_eos_show_ip_arp", false)
	if raw.Success || raw.ErrorType != "runtime" || raw.InputLine != "lab-leaf-1#show ip arp" {
		t.Fatalf("uncleaned strict: %+v", raw)
	}
	if ok := Test(strict, eosARPSession, "arista_eos_show_ip_arp", true); !ok.Success {
		t.Fatalf("cleaned strict: %+v", ok)
	}
	if bad := Test("Value X (\\S+\n\nStart\n  ^${X} -> Record\n", eosARPSession, "", true); bad.Compiled || bad.ErrorType != "compile" {
		t.Fatalf("compile error: %+v", bad)
	}
}

func TestPlatformsCountDistinctNames(t *testing.T) {
	ps, err := Platforms(seedCopy(t))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i, p := range ps {
		counts[p.Platform] = p.Count
		if i > 0 && ps[i-1].Platform >= p.Platform {
			t.Fatalf("not sorted at %q", p.Platform)
		}
	}
	// netlapse's Lab shows "juniper_junos (16)" against this database.
	if counts["juniper_junos"] != 16 {
		t.Errorf("juniper_junos %d, want 16", counts["juniper_junos"])
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	db := seedCopy(t)

	all, err := List(db, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var maxID int64
	nullIDs := 0
	for _, it := range all {
		if it.ID == nil {
			nullIDs++
		} else if *it.ID > maxID {
			maxID = *it.ID
		}
	}
	if nullIDs == 0 {
		t.Fatal("the seed has rows with no id; this test relies on one")
	}

	rowid, id, err := Create(db, "arista_eos_show_lab_arp", arpTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if id != maxID+1 {
		t.Errorf("new id %d, want %d", id, maxID+1)
	}
	row, err := Get(db, rowid)
	if err != nil || row.Name != "arista_eos_show_lab_arp" || row.Content != arpTemplate || row.ID == nil || *row.ID != id {
		t.Fatalf("get after create: %+v, %v", row, err)
	}
	if _, _, err := Create(db, "arista_eos_show_lab_arp", arpTemplate); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate name accepted: %v", err)
	}
	if _, _, err := Create(db, "  ", arpTemplate); err == nil {
		t.Error("empty name accepted")
	}

	// Renaming onto another template's name is refused; onto its own is fine.
	if _, err := Update(db, rowid, "arista_eos_show_ip_arp", arpTemplate); err == nil {
		t.Error("rename onto an existing name accepted")
	}
	if got, err := Update(db, rowid, "arista_eos_show_lab_arp", arpTemplate+"# edited\n"); err != nil || got != id {
		t.Fatalf("update: id %d, %v", got, err)
	}

	// A row with no id gets one on update, and keeps source and hash.
	var nullRow Item
	for _, it := range all {
		if it.ID == nil {
			nullRow = it
			break
		}
	}
	before, _ := Get(db, nullRow.RowID)
	gotID, err := Update(db, nullRow.RowID, before.Name, before.Content)
	if err != nil || gotID != id+1 {
		t.Fatalf("update of null-id row: id %d, %v (want %d)", gotID, err, id+1)
	}
	after, _ := Get(db, nullRow.RowID)
	if after.Source != before.Source || after.Hash != before.Hash || after.ID == nil {
		t.Errorf("update touched more than name, content and id: %+v -> %+v", before, after)
	}

	// Filters.
	junos, _ := List(db, "juniper_junos", "")
	lldp, _ := List(db, "juniper_junos", "LLDP")
	if len(junos) != 16 || len(lldp) == 0 || len(lldp) >= len(junos) {
		t.Errorf("list filters: %d junos, %d junos lldp", len(junos), len(lldp))
	}

	if err := Delete(db, rowid); err != nil {
		t.Fatal(err)
	}
	if err := Delete(db, rowid); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := Get(db, rowid); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if _, err := Update(db, rowid, "x_y", "z"); !errors.Is(err, ErrNotFound) {
		t.Errorf("update after delete: %v", err)
	}
}

// An engine reloaded after a save sees the new template; one not reloaded
// does not. capi relies on the first and exists because of the second.
func TestEngineSeesWritesOnlyAfterReload(t *testing.T) {
	db := seedCopy(t)
	eng, err := tfsmfire.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(db, "arista_eos_show_lab_arp", arpTemplate); err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.Template("arista_eos_show_lab_arp"); ok {
		t.Fatal("engine saw a write without reloading")
	}
	if err := eng.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.Template("arista_eos_show_lab_arp"); !ok {
		t.Fatal("engine did not see the write after reloading")
	}
}
