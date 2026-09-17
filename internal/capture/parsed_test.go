package capture_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/fakedev"
	"github.com/scottpeterman/omegacatqt/internal/tfsmfire"
)

// labARP is IOS "show ip arp" in the shape the device prints it. Lab
// addresses; parsed by the shipped cisco_ios_show_ip_arp template.
const labARP = `Protocol  Address          Age (min)  Hardware Addr   Type   Interface
Internet  172.16.1.1              -   0c1d.5e2f.0001  ARPA   GigabitEthernet0/0
Internet  172.16.1.2              3   0c1d.5e2f.0002  ARPA   GigabitEthernet0/0
Internet  172.16.2.1             12   0c1d.5e2f.0101  ARPA   GigabitEthernet0/1
`

var (
	templatesOnce sync.Once
	templates     *tfsmfire.Engine
	templatesErr  error
)

// shippedTemplates opens the shipped database once for the package: loading
// compiles every template, and each test paying that would make this suite
// the slow one for no added coverage.
func shippedTemplates(t *testing.T) *tfsmfire.Engine {
	t.Helper()
	templatesOnce.Do(func() {
		dir, err := os.MkdirTemp("", "omegacat-templates-")
		if err != nil {
			templatesErr = err
			return
		}
		path, _, err := tfsmfire.EnsureDB(dir)
		if err != nil {
			templatesErr = err
			return
		}
		templates, templatesErr = tfsmfire.Open(path)
	})
	if templatesErr != nil {
		t.Fatalf("template database: %v", templatesErr)
	}
	return templates
}

func labWithARP(name, arp string) fakedev.Config {
	cfg := lab(name)
	cfg.Commands["show ip arp"] = arp
	return cfg
}

// engineOn builds an engine over an existing store, so a test can capture the
// same device into the same history more than once.
func engineOn(t *testing.T, st *capture.FileStore, cfg capture.Config) *capture.Engine {
	t.Helper()
	cfg.Store = st
	cfg.SessionOpts.CommandTimeout = 5 * time.Second
	cfg.SessionOpts.PagingDisable = "terminal length 0"
	e, err := capture.New(cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e
}

func TestAnARPTableIsParsedBesideTheStoredFile(t *testing.T) {
	srv := start(t, labWithARP("lab-r1", labARP))
	run := capturerun.New()
	e, st := engine(t, capture.Config{
		Dial:   dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs:  []capture.Spec{capture.RunningConfig, capture.ARPTable},
		Parser: shippedTemplates(t),
		Emit:   run.Emit(),
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})
	run.Finish()

	var arp, cfg capture.Result
	for _, r := range res {
		switch r.Type {
		case "arp-table":
			arp = r
		case "running-config":
			cfg = r
		}
	}
	if arp.Err != nil {
		t.Fatalf("arp capture: %v", arp.Err)
	}
	if arp.Parsed == nil || arp.Parsed.Status != capture.ParseStatusParsed {
		t.Fatalf("arp-table not parsed: %+v", arp.Parsed)
	}
	p := arp.Parsed
	if !strings.HasPrefix(p.Template, "cisco_ios_show_") || !strings.Contains(p.Template, "arp") {
		t.Errorf("parsed with %q, want an IOS ARP template", p.Template)
	}
	if len(p.Records) != 3 || p.Score < capture.MinParseScore {
		t.Errorf("parse: %d records, score %.1f; want 3 records", len(p.Records), p.Score)
	}
	if p.RawSHA256 != arp.Artifact.SHA256 || p.RawFile != filepath.Base(arp.Artifact.Path) {
		t.Errorf("sidecar not tied to its capture: raw %s %s, artifact %s %s",
			p.RawFile, p.RawSHA256, filepath.Base(arp.Artifact.Path), arp.Artifact.SHA256)
	}

	// On disk, readable back through the store by the capture's file name.
	got, ok, err := st.ReadParsed("lab-r1", "arp-table", p.RawFile)
	if err != nil || !ok || len(got.Records) != 3 {
		t.Fatalf("ReadParsed: ok=%v err=%v records=%d", ok, err, len(got.Records))
	}
	found := false
	for _, r := range got.Records {
		if strings.Contains(strings.ToLower(asString(r)), "0c1d.5e2f.0101") {
			found = true
		}
	}
	if !found {
		t.Errorf("parsed records do not carry the MAC from the table: %v", got.Records)
	}

	// A config is not a parsed type: no sidecar, no parse fields.
	if cfg.Parsed != nil {
		t.Errorf("running-config was parsed: %+v", cfg.Parsed)
	}
	if _, ok, _ := st.ReadParsed("lab-r1", "running-config", filepath.Base(cfg.Artifact.Path)); ok {
		t.Error("running-config has a sidecar")
	}

	// The run row carries the outcome, for the table a view shows.
	for _, row := range run.Rows() {
		switch row.Type {
		case "arp-table":
			if row.ParseStatus != "parsed" || row.ParseRecords != 3 || row.Template != p.Template {
				t.Errorf("arp row parse fields: %+v", row)
			}
		case "running-config":
			if row.ParseStatus != "" {
				t.Errorf("running-config row has parse status %q", row.ParseStatus)
			}
		}
	}
}

func asString(r tfsmfire.Record) string {
	var b strings.Builder
	for _, v := range r {
		switch x := v.(type) {
		case string:
			b.WriteString(x + " ")
		case []string:
			b.WriteString(strings.Join(x, " ") + " ")
		}
	}
	return b.String()
}

// Output no template can read is still a stored capture. The parse says so,
// with the closest template, and the capture itself succeeds.
func TestAnUnparseableTableStillStores(t *testing.T) {
	srv := start(t, labWithARP("lab-r1", "this build prints nothing any template knows\nreally nothing\n"))
	e, st := engine(t, capture.Config{
		Dial:   dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs:  []capture.Spec{capture.ARPTable},
		Parser: shippedTemplates(t),
	})
	res := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})
	if res[0].Err != nil {
		t.Fatalf("capture failed over a parse: %v", res[0].Err)
	}
	if res[0].Parsed == nil || res[0].Parsed.Status != capture.ParseStatusNoMatch || len(res[0].Parsed.Records) != 0 {
		t.Fatalf("parse of unreadable output: %+v", res[0].Parsed)
	}
	if h, _ := st.History("lab-r1", "arp-table"); len(h) != 1 {
		t.Errorf("history has %d entries, want the stored capture", len(h))
	}
}

// An unchanged capture writes no new file but does parse again, so a template
// fixed since the file was stored applies to it tonight.
func TestAnUnchangedTableIsParsedAgain(t *testing.T) {
	srv := start(t, labWithARP("lab-r1", labARP))
	e, st := engine(t, capture.Config{
		Dial:   dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
		Specs:  []capture.Spec{capture.ARPTable},
		Parser: shippedTemplates(t),
	})
	first := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})[0]
	time.Sleep(10 * time.Millisecond)
	second := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})[0]

	if !second.Artifact.Unchanged {
		t.Fatalf("second capture not unchanged")
	}
	if second.Parsed == nil || second.Parsed.RawFile != first.Parsed.RawFile {
		t.Fatalf("unchanged capture's parse names %+v, want the matched file %s", second.Parsed, first.Parsed.RawFile)
	}
	got, ok, _ := st.ReadParsed("lab-r1", "arp-table", first.Parsed.RawFile)
	if !ok || !got.ParsedAt.After(first.Parsed.ParsedAt) {
		t.Errorf("sidecar not rewritten on the unchanged capture: %v then %v", first.Parsed.ParsedAt, got.ParsedAt)
	}
}

// Retention removes a parse with the capture it describes.
func TestPruningRemovesParsesWithTheirCaptures(t *testing.T) {
	st, err := capture.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := capture.ARPTable
	spec.Keep = 1

	for i, arp := range []string{labARP, strings.Replace(labARP, "0c1d.5e2f.0101", "0c1d.5e2f.0202", 1)} {
		srv := start(t, labWithARP("lab-r1", arp))
		e := engineOn(t, st, capture.Config{
			Dial:   dialerFor(t, map[string]*fakedev.Server{"lab-r1": srv}),
			Specs:  []capture.Spec{spec},
			Parser: shippedTemplates(t),
		})
		if r := e.Capture(context.Background(), []capture.Device{{Target: "lab-r1"}})[0]; r.Err != nil || r.Parsed == nil {
			t.Fatalf("capture %d: %v parsed=%v", i, r.Err, r.Parsed)
		}
		time.Sleep(1100 * time.Millisecond) // a distinct timestamp for the next file
	}

	entries, err := os.ReadDir(filepath.Join(st.Root(), "devices", "lab-r1", "arp-table"))
	if err != nil {
		t.Fatal(err)
	}
	var txt, parsed int
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".parsed.json"):
			parsed++
		case strings.HasSuffix(e.Name(), ".txt"):
			txt++
		}
	}
	if txt != 1 || parsed != 1 {
		t.Errorf("after pruning to 1 version: %d captures and %d parses on disk, want 1 and 1", txt, parsed)
	}
}

// Every template hint a builtin carries selects at least one template that
// runs, in the database that ships. A hint that selects nothing stores tables
// forever unparsed with no error anywhere; a hint whose only templates do not
// compile in Go is the same failure one step later.
func TestEveryTemplateHintSelectsARunnableTemplate(t *testing.T) {
	eng := shippedTemplates(t)
	for _, spec := range capture.Builtin() {
		for platform, cmd := range spec.Commands {
			if cmd.TemplateHint == "" {
				continue
			}
			runnable := 0
			for _, tpl := range eng.Filter(cmd.TemplateHint) {
				if tpl.CompileErr == "" {
					runnable++
				}
			}
			if runnable == 0 {
				t.Errorf("%s/%s: hint %q selects no runnable template", spec.Type, platform, cmd.TemplateHint)
			}
		}
	}
}
