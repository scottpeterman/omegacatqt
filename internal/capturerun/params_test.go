// internal/capturerun/params_test.go
//
// The parameters, the device list, and the identity rules applied to it.
//
// The resolver is injected throughout, so the CGNAT rule is tested without DNS
// — which is also how it has to work in the lab it runs in, where the names
// have no DNS behind them at all.
package capturerun_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/capturerun"
)

// fakeResolver answers only what it is told to, so a test that expects no
// lookup can prove none happened.
type fakeResolver struct {
	ptr     map[string][]string
	forward map[string][]string
	calls   int
}

func (f *fakeResolver) LookupAddr(addr string) ([]string, error) {
	f.calls++
	if names, ok := f.ptr[addr]; ok {
		return names, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeResolver) LookupHost(host string) ([]string, error) {
	f.calls++
	if addrs, ok := f.forward[host]; ok {
		return addrs, nil
	}
	return nil, os.ErrNotExist
}

func fieldsOf(errs []capturerun.ValidationError) map[string]bool {
	out := map[string]bool{}
	for _, e := range errs {
		out[e.Field] = true
	}
	return out
}

func validParams(t *testing.T) capturerun.Params {
	t.Helper()
	p := capturerun.Defaults()
	p.Devices = []string{"lab-r1.lab.example"}
	p.Types = []string{"running-config"}
	p.StorePath = t.TempDir()
	return p
}

// A device list is pasted from wherever it lives. Anything that separates two
// device names in the wild has to work here, or the first thing someone does
// with the form is reformat a list by hand.
func TestParseDevicesAcceptsEverySeparatorAndDropsComments(t *testing.T) {
	got := capturerun.ParseDevices(`
# the core, do not reorder
lab-r1.lab.example
lab-r2.lab.example, lab-r3.lab.example
lab-sw1.lab.example;lab-sw2.lab.example	172.16.1.2

lab-r1.lab.example          # already listed above
# lab-r9.lab.example        # decommissioned 2026-07
`)
	want := []string{
		"lab-r1.lab.example", "lab-r2.lab.example", "lab-r3.lab.example",
		"lab-sw1.lab.example", "lab-sw2.lab.example", "172.16.1.2",
	}
	if len(got) != len(want) {
		t.Fatalf("ParseDevices returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseDevices returned %v, want %v", got, want)
		}
	}
}

// The commented-out device is the one that matters. A list someone maintains
// by hand accumulates lines explaining why a box is not on it, and a parser
// that ignores comments quietly puts every decommissioned device back.
func TestACommentedDeviceStaysOut(t *testing.T) {
	for _, in := range []string{
		"# lab-r9.lab.example",
		"   #lab-r9.lab.example",
		"lab-r1.lab.example # replaced lab-r9.lab.example",
	} {
		for _, d := range capturerun.ParseDevices(in) {
			if strings.Contains(d, "lab-r9") {
				t.Errorf("%q yielded the commented device %q", in, d)
			}
		}
	}
}

func TestLoadDeviceFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.txt")
	if err := os.WriteFile(path, []byte("lab-r1.lab.example\n# a note\nlab-r2.lab.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := capturerun.LoadDeviceFile(path)
	if err != nil {
		t.Fatalf("LoadDeviceFile: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want 2 devices", got)
	}

	if _, err := capturerun.LoadDeviceFile(filepath.Join(dir, "nope.txt")); err == nil {
		t.Error("a missing device list was not an error; the run would visit nothing and look healthy")
	}

	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("# everything is commented out\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := capturerun.LoadDeviceFile(empty); err == nil {
		t.Error("a device list with no devices in it was accepted")
	}
}

// Inline entries and the file are one list, deduplicated. A device named in
// both is one device, not two visits and two logins.
func TestTargetsMergesInlineAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.txt")
	if err := os.WriteFile(path, []byte("lab-r2.lab.example\nlab-r1.lab.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := capturerun.Defaults()
	p.Devices = []string{"lab-r1.lab.example"}
	p.DeviceFile = path

	got, err := p.Targets()
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	want := []string{"lab-r1.lab.example", "lab-r2.lab.example"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Targets = %v, want %v", got, want)
	}
}

// A CGNAT address is reverse resolved and the name adopted only if it also
// resolves forward. Shared address space is recycled, so an address is not an
// identity: two devices behind different translations can wear the same
// 100.64 address and their configs would land in one history.
func TestTheCGNATRuleIsAppliedToTheDeviceList(t *testing.T) {
	r := &fakeResolver{
		ptr: map[string][]string{
			"100.64.4.9":  {"lab-sw1.lab.example."},
			"100.64.4.10": {"lab-stale.lab.example."},
		},
		forward: map[string][]string{
			"lab-sw1.lab.example": {"100.64.4.9"},
		},
	}
	p := capturerun.Defaults()
	p.Domains = []string{"lab.example"}
	p.Devices = []string{
		"100.64.4.9",         // CGNAT, forward-confirmed
		"100.64.4.10",        // CGNAT, stale PTR
		"100.64.4.11",        // CGNAT, no PTR
		"172.16.1.2",         // ordinary address, no lookup
		"lab-r1.lab.example", // a name, no lookup
	}

	got, err := p.ResolveTargetsWith(r)
	if err != nil {
		t.Fatalf("ResolveTargetsWith: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d targets, want 5", len(got))
	}

	confirmed := got[0]
	if confirmed.Identity != "lab-sw1" {
		t.Errorf("confirmed CGNAT Identity = %q, want the suffix-stripped name", confirmed.Identity)
	}
	if confirmed.Dial != "100.64.4.9" {
		t.Errorf("confirmed CGNAT Dial = %q; the address is what is known to work", confirmed.Dial)
	}
	if confirmed.Addr != "100.64.4.9" {
		t.Errorf("confirmed CGNAT Addr = %q", confirmed.Addr)
	}
	if confirmed.Note == "" {
		t.Error("a CGNAT address that became a name said nothing about it")
	}

	for i, want := range map[int]string{1: "100.64.4.10", 2: "100.64.4.11"} {
		if got[i].Identity != want {
			t.Errorf("unconfirmed CGNAT %d Identity = %q, want the address kept", i, got[i].Identity)
		}
		if got[i].Note == "" {
			t.Errorf("unconfirmed CGNAT %d gave no reason for keeping the address", i)
		}
	}

	if got[3].Identity != "172.16.1.2" || got[3].Note != "" {
		t.Errorf("an ordinary address was rewritten: %+v", got[3])
	}
	if got[4].Identity != "lab-r1" {
		t.Errorf("a name outside CGNAT got Identity %q, want the suffix stripped only", got[4].Identity)
	}

	// Two lookups for the confirmed address (reverse then forward), two for
	// the stale one, one for the address with no PTR, and none at all for
	// the ordinary address or the name. Counting them is the only way to
	// show the non-CGNAT entries cost nothing.
	if r.calls != 5 {
		t.Errorf("%d lookups, want 5: only the three CGNAT entries should cost anything", r.calls)
	}
}

// Names are never resolved. This lab has no DNS behind its names at all, so a
// lookup here would be a cost that buys nothing and a failure that means
// nothing.
func TestNamesAreNeverResolved(t *testing.T) {
	r := &fakeResolver{}
	p := capturerun.Defaults()
	p.Devices = []string{"lab-r1.lab.local", "lab-r2.lab.local", "wan-core-1"}
	if _, err := p.ResolveTargetsWith(r); err != nil {
		t.Fatalf("ResolveTargetsWith: %v", err)
	}
	if r.calls != 0 {
		t.Errorf("resolving a list of names cost %d lookups, want 0", r.calls)
	}
}

// A device written short in one list and qualified in another is one device.
func TestSuffixStrippingMakesOneDeviceOfTwoSpellings(t *testing.T) {
	p := capturerun.Defaults()
	p.Domains = []string{".lab.example"} // written with the leading dot on purpose
	p.Normalize()
	p.Devices = []string{"lab-r1.lab.example", "lab-r2"}

	got, err := p.ResolveTargetsWith(&fakeResolver{})
	if err != nil {
		t.Fatalf("ResolveTargetsWith: %v", err)
	}
	if got[0].Identity != "lab-r1" {
		t.Errorf("Identity = %q, want the suffix stripped", got[0].Identity)
	}
	if got[0].Dial != "lab-r1.lab.example" {
		t.Errorf("Dial = %q, want the qualified name kept — that is what resolves", got[0].Dial)
	}
	if got[1].Identity != "lab-r2" {
		t.Errorf("Identity = %q, want the short name untouched", got[1].Identity)
	}
}

// Validate reports every problem at once so a form marks all the bad fields in
// one pass rather than one per attempt.
func TestValidateReportsEveryProblem(t *testing.T) {
	var p capturerun.Params
	p.Concurrency = 0
	p.ExpensiveConcurrency = 0
	p.Timeout = 0
	p.HostKeys = "whatever"

	errs := p.Validate()
	fields := fieldsOf(errs)
	for _, want := range []string{
		"devices", "store_path", "concurrency", "expensive_concurrency",
		"timeout", "host_keys",
	} {
		if !fields[want] {
			t.Errorf("no error for %s; got %v", want, errs)
		}
	}
	for _, e := range errs {
		if e.Error() == "" || e.Message == "" {
			t.Errorf("empty validation message on %q", e.Field)
		}
	}
}

func TestValidAndDefaultParams(t *testing.T) {
	p := validParams(t)
	if errs := p.Validate(); len(errs) != 0 {
		t.Errorf("a valid capture did not validate: %v", errs)
	}
	if d := capturerun.Defaults(); d.HostKeys != capturerun.HostKeyStrict {
		t.Errorf("Defaults().HostKeys = %q; a capture works from a list of known "+
			"devices, so strict is the defensible default", d.HostKeys)
	}
	if capturerun.Defaults().ExpensiveConcurrency != 1 {
		t.Error("the expensive lane does not default to 1")
	}
}

// Insecure gets its own message. Refusing it with "unknown mode" would read as
// a typo rather than as a decision.
func TestInsecureHostKeysIsRefusedWithAReason(t *testing.T) {
	p := validParams(t)
	p.HostKeys = "insecure"
	errs := p.Validate()
	if !fieldsOf(errs)["host_keys"] {
		t.Fatal("insecure host keys were accepted")
	}
	for _, e := range errs {
		if e.Field == "host_keys" && !strings.Contains(e.Message, "changed") {
			t.Errorf("the refusal does not say why: %q", e.Message)
		}
	}
}

// An expensive lane wider than the device lane bounds nothing, which is the
// one thing it exists to do.
func TestAnExpensiveLaneThatBoundsNothingIsFlagged(t *testing.T) {
	p := validParams(t)
	p.Concurrency = 5
	p.ExpensiveConcurrency = 10
	if !fieldsOf(p.Validate())["expensive_concurrency"] {
		t.Error("an expensive lane wider than the device lane was accepted silently")
	}
}

// A capture with nowhere to store is a connection test. Saying so is more
// useful than letting it run and produce nothing.
func TestAStoreIsRequired(t *testing.T) {
	p := validParams(t)
	p.StorePath = ""
	if !fieldsOf(p.Validate())["store_path"] {
		t.Error("a capture with no store was accepted")
	}
}

// A capture is told its scope. An empty device list is not "everything".
func TestADeviceListIsRequired(t *testing.T) {
	p := validParams(t)
	p.Devices = nil
	if !fieldsOf(p.Validate())["devices"] {
		t.Error("a capture with no devices was accepted")
	}
}

// A device file named but absent is caught while the dialog is open, not at
// the first dial.
func TestAMissingDeviceFileIsAValidationError(t *testing.T) {
	p := validParams(t)
	p.DeviceFile = filepath.Join(t.TempDir(), "absent.txt")
	if !fieldsOf(p.Validate())["device_file"] {
		t.Error("a device file that cannot be read was accepted")
	}
	p.DeviceFile = t.TempDir()
	if !fieldsOf(p.Validate())["device_file"] {
		t.Error("a directory was accepted as a device file")
	}
}

// This package cannot import capture — capture imports it — so the type names
// are checked for shape here and against the real set by whoever knows it.
func TestTypeNamesAreCheckedForShapeAndThenAgainstTheRealSet(t *testing.T) {
	p := validParams(t)
	p.Types = []string{"Running Config"}
	if !fieldsOf(p.Validate())["types"] {
		t.Error("a type name that is not a type name was accepted")
	}

	p.Types = []string{"running-config", "runing-config"}
	if errs := p.Validate(); fieldsOf(errs)["types"] {
		t.Errorf("shape validation rejected a well-formed name: %v", errs)
	}
	errs := p.ValidateAgainst([]string{"running-config", "startup-config", "inventory"})
	if !fieldsOf(errs)["types"] {
		t.Fatal("a typo'd capture type passed ValidateAgainst")
	}
	var found bool
	for _, e := range errs {
		if e.Field == "types" && strings.Contains(e.Message, "startup-config") {
			found = true
		}
	}
	if !found {
		t.Error("the unknown-type error does not say what the known types are")
	}
}

// ValidateAgainst with nothing known is Validate. A caller that has not been
// given a type list must not have every type rejected.
func TestValidateAgainstNothingKnownIsJustValidate(t *testing.T) {
	p := validParams(t)
	if errs := p.ValidateAgainst(nil); len(errs) != 0 {
		t.Errorf("ValidateAgainst(nil) rejected a valid capture: %v", errs)
	}
}

// Credential tags without a vault are a setting that silently does nothing,
// which is worse than an error.
func TestCredTagsWithoutAVaultAreFlagged(t *testing.T) {
	p := validParams(t)
	p.CredTags = []string{"lab-ro"}
	if !fieldsOf(p.Validate())["cred_tags"] {
		t.Error("credential tags with no vault were accepted")
	}
}

// Normalize is what stops two profiles differing only by whitespace, and what
// makes ".lab.example" and "lab.example" the same intent.
func TestNormalize(t *testing.T) {
	p := capturerun.Params{
		Devices:    []string{" lab-r1.lab.example ", "lab-r1.lab.example", "  "},
		Types:      []string{"Running-Config", "running-config"},
		Domains:    []string{".Lab.Example", "lab.example"},
		StorePath:  "  /tmp/captures  ",
		DeviceFile: " /tmp/devices.txt ",
	}
	p.Normalize()

	if len(p.Devices) != 1 || p.Devices[0] != "lab-r1.lab.example" {
		t.Errorf("Devices = %v, want one trimmed entry", p.Devices)
	}
	if len(p.Types) != 1 || p.Types[0] != "running-config" {
		t.Errorf("Types = %v, want one lowercased entry", p.Types)
	}
	if len(p.Domains) != 1 || p.Domains[0] != "lab.example" {
		t.Errorf("Domains = %v, want the leading dot and the duplicate gone", p.Domains)
	}
	if p.StorePath != "/tmp/captures" || p.DeviceFile != "/tmp/devices.txt" {
		t.Errorf("paths were not trimmed: %q %q", p.StorePath, p.DeviceFile)
	}
	if p.HostKeys != capturerun.HostKeyStrict {
		t.Errorf("HostKeys = %q, want the default filled in", p.HostKeys)
	}
}

// Profiles are what make a form worth filling in once, which is the whole
// argument for a capture having a UI rather than a cron line.
func TestProfilesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "capture-profiles.json")
	pr, err := capturerun.OpenProfiles(path)
	if err != nil {
		t.Fatalf("OpenProfiles: %v", err)
	}
	if len(pr.Names()) != 0 {
		t.Error("a missing profile file did not open empty")
	}

	nightly := validParams(t)
	nightly.Types = []string{"running-config", "startup-config"}
	if err := pr.Save("nightly", nightly); err != nil {
		t.Fatalf("Save: %v", err)
	}
	weekly := validParams(t)
	weekly.Types = []string{"tech-support"}
	if err := pr.Save("weekly", weekly); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := capturerun.OpenProfiles(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := again.Get("nightly")
	if !ok {
		t.Fatal("nightly did not survive the round trip")
	}
	if len(got.Params.Types) != 2 {
		t.Errorf("Types = %v, want both", got.Params.Types)
	}

	// Most recently used first, which is the order a dropdown wants.
	if names := again.Names(); names[0] != "weekly" {
		t.Errorf("Names() = %v, want the most recent first", names)
	}
	time.Sleep(2 * time.Millisecond)
	if err := again.Touch("nightly"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if names := again.Names(); names[0] != "nightly" {
		t.Errorf("Names() = %v after touching nightly", names)
	}

	if err := again.Delete("weekly"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := again.Get("weekly"); ok {
		t.Error("weekly survived deletion")
	}
	if err := again.Delete("weekly"); err != nil {
		t.Errorf("deleting an absent profile errored: %v", err)
	}
	if err := again.Save("  ", validParams(t)); err == nil {
		t.Error("a profile with no name was saved")
	}
}

// A corrupt profile file is an error, not an empty list. An empty list looks
// exactly like a first run, and the response to a first run is to type
// everything in again.
func TestACorruptProfileFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := capturerun.OpenProfiles(path); err == nil {
		t.Error("a corrupt profile file opened as an empty one")
	}

	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := capturerun.OpenProfiles(empty); err != nil {
		t.Errorf("an empty profile file errored: %v", err)
	}
}

// Session keys stand in for patterns when selecting from a session file, and
// like patterns they mean nothing without one.
func TestSessionKeysSelectFromASessionFile(t *testing.T) {
	inv := filepath.Join(t.TempDir(), "inventory.yaml")
	if err := os.WriteFile(inv, []byte("version: 1\nfolders: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := validParams(t)
	p.Devices = nil
	p.SessionFile = inv
	if f := fieldsOf(p.Validate()); !f["match"] {
		t.Fatalf("a session file with neither patterns nor keys was accepted: %v", f)
	}

	p.SessionKeys = []string{" SSH:127.0.0.1:2201 ", "ssh:127.0.0.1:2201"}
	p.Normalize()
	if f := fieldsOf(p.Validate()); len(f) != 0 {
		t.Fatalf("keys alone should select from a session file: %v", f)
	}
	if len(p.SessionKeys) != 1 || p.SessionKeys[0] != "ssh:127.0.0.1:2201" {
		t.Fatalf("keys not cleaned: %q", p.SessionKeys)
	}

	p.SessionFile = ""
	p.Devices = []string{"lab-r1"}
	if f := fieldsOf(p.Validate()); !f["session_keys"] {
		t.Fatalf("keys with no session file were accepted: %v", f)
	}
}
