package tfsmfire

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
)

// The user's copy is theirs: a second EnsureDB must not replace it, however
// different it is from the seed.
func TestEnsureDBNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path, created, err := EnsureDB(dir)
	if err != nil || !created {
		t.Fatalf("first EnsureDB: %v created=%v", err, created)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, Seed()) {
		t.Fatal("first-run copy is not the seed")
	}
	if err := os.WriteFile(path, []byte("edited by the user"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, created, err := EnsureDB(dir); err != nil || created {
		t.Fatalf("second EnsureDB: %v created=%v", err, created)
	}
	if b, _ := os.ReadFile(path); string(b) != "edited by the user" {
		t.Fatal("EnsureDB replaced an existing database")
	}
}

// The filter is SQLite LIKE per term: terms of two characters or fewer are
// dropped, '-' splits like '_', and case folds for ASCII.
func TestFilterSemantics(t *testing.T) {
	e := seedEngine(t)
	names := func(hint string) map[string]bool {
		out := map[string]bool{}
		for _, tpl := range e.Filter(hint) {
			out[tpl.Name] = true
		}
		return out
	}
	eos := names("arista_eos")
	if !eos["arista_eos_show_ip_arp"] || eos["cisco_ios_show_ip_arp"] {
		t.Fatalf("arista_eos selected the wrong set (%d)", len(eos))
	}
	if len(names("ARISTA_EOS")) != len(eos) {
		t.Error("the filter is case-sensitive")
	}
	// "ip" is two characters and dropped: this is the same filter as
	// arista_eos_show_arp.
	if a, b := len(names("arista_eos_show_ip_arp")), len(names("arista_eos_show_arp")); a != b {
		t.Errorf("short terms not dropped: %d vs %d", a, b)
	}
	// '-' splits like '_'.
	if a, b := len(names("arista-eos-show-mac")), len(names("arista_eos_show_mac")); a != b || a == 0 {
		t.Errorf("hyphen not treated as a separator: %d vs %d", a, b)
	}
	if len(names("")) != len(e.Templates()) {
		t.Error("an empty hint should select every template")
	}
}

func TestFindBestUsesTheHint(t *testing.T) {
	e := seedEngine(t)
	arp, _ := e.Template("cisco_ios_show_ip_arp")
	m := e.FindBest(arp.Sample, "cisco_ios")
	if m.Template != "cisco_ios_show_ip_arp" || len(m.Records) == 0 || m.Tried == 0 {
		t.Fatalf("cisco_ios hint on an IOS ARP table: %+v", m)
	}
	if m := e.FindBest(arp.Sample, "no_such_platform"); m.Template != "" || m.Tried != 0 {
		t.Errorf("a hint that selects nothing produced %+v", m)
	}
}

// FindBest from many goroutines at once over the same templates. gotextfsm
// keeps parse state in the compiled template, so this is the test that
// catches a future "optimization" caching compiled templates. Needs -race.
func TestFindBestIsSafeConcurrently(t *testing.T) {
	e := seedEngine(t)
	mac, _ := e.Template("arista_eos_show_mac_address-table")
	arp, _ := e.Template("arista_eos_show_ip_arp")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sample, want := arp.Sample, "arista_eos_show_ip_arp"
			if i%2 == 0 {
				sample, want = mac.Sample, "arista_eos_show_mac_address-table"
			}
			if m := e.FindBest(sample, "arista_eos_show_"+strings.Split(want, "_")[3]); m.Template != want {
				t.Errorf("goroutine %d: got %q, want %q", i, m.Template, want)
			}
		}(i)
	}
	wg.Wait()
}

func TestCleanOutputAfterTheLastEcho(t *testing.T) {
	raw := strings.Join([]string{
		"lab-r1#terminal length 0",
		"lab-r1#show ip arp",
		"Protocol  Address          Age (min)  Hardware Addr   Type   Interface",
		"Internet  172.16.1.1              -   0c1d.5e2f.0001  ARPA   GigabitEthernet0/0",
		"",
		"lab-r1#",
	}, "\n")
	got := CleanOutput(raw)
	want := "Protocol  Address          Age (min)  Hardware Addr   Type   Interface\n" +
		"Internet  172.16.1.1              -   0c1d.5e2f.0001  ARPA   GigabitEthernet0/0"
	if got != want {
		t.Fatalf("CleanOutput =\n%q\nwant\n%q", got, want)
	}
}

func TestCleanOutputWithoutAnEcho(t *testing.T) {
	raw := "terminal length 0\n\n--- JUNOS 21.4R3 built 2023\nshow arp no-resolve\n" +
		"MAC Address       Address         Interface\n0c:1d:5e:2f:00:01 172.16.1.1      ge-0/0/0.0\nlab@lab-mx1>\n\n"
	want := "MAC Address       Address         Interface\n0c:1d:5e:2f:00:01 172.16.1.1      ge-0/0/0.0"
	if got := CleanOutput(raw); got != want {
		t.Fatalf("CleanOutput =\n%q\nwant\n%q", got, want)
	}
}

func TestTestTemplateReportsBothErrorKinds(t *testing.T) {
	bad := TestTemplate("Value X ((?<!a)b)\n\nStart\n  ^${X} -> Record\n", "b", "")
	if bad.Compiled || bad.ErrorType != "compile" || bad.Error == "" {
		t.Errorf("compile failure: %+v", bad)
	}

	strict := "Value X (\\d+)\n\nStart\n  ^${X} -> Record\n  ^. -> Error\n"
	run := TestTemplate(strict, "1\nnot a number\n", "")
	if !run.Compiled || run.Success || run.ErrorType != "runtime" || run.RuleLine == 0 || run.InputLine == "" {
		t.Errorf("runtime failure: %+v", run)
	}

	ok := TestTemplate(strict, "1\n2\n3\n", "")
	if !ok.Success || ok.RecordCount != 3 || ok.FieldCount != 1 || ok.Score != ok.Breakdown.Total || ok.Score == 0 {
		t.Errorf("success: %+v", ok)
	}
}
