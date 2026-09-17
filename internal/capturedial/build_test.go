// internal/capturedial/build_test.go
//
// Build's job is that both front ends get the same capture from the same
// intent, so these tests are mostly about the ways that can quietly stop being
// true.
//
// Nothing here connects to anything. The engine's behaviour against real SSH
// servers is capture_test.go's job; this is about assembly.
package capturedial_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturedial"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
)

type fakeResolver struct {
	ptr     map[string][]string
	forward map[string][]string
}

func (f *fakeResolver) LookupAddr(addr string) ([]string, error) {
	if n, ok := f.ptr[addr]; ok {
		return n, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeResolver) LookupHost(host string) ([]string, error) {
	if a, ok := f.forward[host]; ok {
		return a, nil
	}
	return nil, os.ErrNotExist
}

func validParams(t *testing.T) capturerun.Params {
	t.Helper()
	p := capturerun.Defaults()
	p.Devices = []string{"lab-r1.lab.example", "lab-r2.lab.example"}
	p.StorePath = filepath.Join(t.TempDir(), "captures")
	return p
}

// One error per run is one round trip per mistake. A CLI prints them all and a
// form marks them all, and both get the same list because both call Build.
func TestBuildRefusesInvalidParametersAndListsThemAll(t *testing.T) {
	var p capturerun.Params // everything zero
	_, err := capturedial.Build(p, capturedial.Options{})
	if err == nil {
		t.Fatal("Build accepted entirely empty parameters")
	}
	for _, want := range []string{"devices", "store_path", "concurrency", "timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %s:\n%v", want, err)
		}
	}
	// An empty host-key mode is not a problem — Normalize fills in the
	// default. A wrong one is, and so is the one that means something.
	bad := validParams(t)
	bad.HostKeys = "insecure"
	if _, err := capturedial.Build(bad, capturedial.Options{DNS: &fakeResolver{}}); err == nil {
		t.Error("Build accepted insecure host-key handling")
	} else if !strings.Contains(err.Error(), "changed") {
		t.Errorf("the refusal does not say why insecure is different:\n%v", err)
	}
}

// A typo'd capture type is caught here rather than at the first dial, and the
// message says what the alternatives are — the point at which someone realises
// they wrote "runing-config" is the point they want the list.
func TestAnUnknownCaptureTypeIsRefusedWithTheKnownSet(t *testing.T) {
	p := validParams(t)
	p.Types = []string{"runing-config"}
	_, err := capturedial.Build(p, capturedial.Options{})
	if err == nil {
		t.Fatal("Build accepted a capture type that does not exist")
	}
	if !strings.Contains(err.Error(), "running-config") {
		t.Errorf("the error does not list the known types:\n%v", err)
	}
}

// An empty Types is not "everything". Defaulting to every builtin would put a
// tech-support on every device in the estate because a field was left blank.
func TestSpecsForDefaultsToTheCheapConfigCapture(t *testing.T) {
	specs, err := capturedial.SpecsFor(nil)
	if err != nil {
		t.Fatalf("SpecsFor(nil): %v", err)
	}
	if len(specs) != 1 || specs[0].Type != capture.RunningConfig.Type {
		t.Fatalf("SpecsFor(nil) = %v, want just the running config", specs)
	}
	for _, cmd := range specs[0].Commands {
		if cmd.Cost == capture.CostExpensive {
			t.Errorf("the default capture declares an expensive command on %s", specs[0].Type)
		}
	}

	// And it honours what it is given, in the order given.
	specs, err = capturedial.SpecsFor([]string{"inventory", "running-config"})
	if err != nil {
		t.Fatalf("SpecsFor: %v", err)
	}
	if len(specs) != 2 || specs[0].Type != "inventory" {
		t.Errorf("SpecsFor did not preserve the caller's order: %v", specs)
	}
}

func TestKnownTypesCoversEveryBuiltinAndIsStable(t *testing.T) {
	got := capturedial.KnownTypes()
	if len(got) != len(capture.Builtin()) {
		t.Errorf("KnownTypes has %d entries, capture.Builtin has %d",
			len(got), len(capture.Builtin()))
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("KnownTypes is unsorted, so the error message it appears in "+
			"changes between runs: %v", got)
	}
	for _, typ := range got {
		if _, ok := capture.Lookup(typ); !ok {
			t.Errorf("KnownTypes advertises %q, which Lookup does not know", typ)
		}
	}
}

// The Identity on a device is the RUN key — the string every event is filed
// under. Dial is what actually gets connected to, and for a CGNAT address that
// resolved they are deliberately different: the name identifies the box, the
// address is what is known to answer.
func TestDevicesCarryTheRunIdentityAndTheDialTargetSeparately(t *testing.T) {
	r := &fakeResolver{
		ptr:     map[string][]string{"100.64.4.9": {"lab-sw1.lab.example."}},
		forward: map[string][]string{"lab-sw1.lab.example": {"100.64.4.9"}},
	}
	p := capturerun.Defaults()
	p.Domains = []string{"lab.example"}
	p.Devices = []string{"100.64.4.9", "lab-r1.lab.example"}

	devices, notes, _, err := capturedial.Devices(p, r)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(devices))
	}
	if devices[0].Identity != "lab-sw1" {
		t.Errorf("Identity = %q, want the confirmed name", devices[0].Identity)
	}
	if devices[0].Target != "100.64.4.9" {
		t.Errorf("Target = %q, want the address that is known to answer", devices[0].Target)
	}
	if devices[0].Addr != "100.64.4.9" {
		t.Errorf("Addr = %q", devices[0].Addr)
	}
	if len(devices[0].Aliases) == 0 {
		t.Error("the resolved name and the address were not offered as aliases, " +
			"so the binding store cannot match this device by either")
	}
	if notes["lab-sw1"] == "" {
		t.Error("a CGNAT address that became a name reported nothing about it")
	}
	if devices[1].Identity != "lab-r1" || devices[1].Target != "lab-r1.lab.example" {
		t.Errorf("a plain name became %+v", devices[1])
	}
	if notes["lab-r1"] != "" {
		t.Error("a device with nothing to report produced a note")
	}
}

// Build with no vault is the harness's own path and has to work end to end up
// to the point of dialling.
func TestBuildAssemblesAnEngineAStoreAndASpecSet(t *testing.T) {
	p := validParams(t)
	p.Types = []string{"running-config", "inventory"}

	built, err := capturedial.Build(p, capturedial.Options{
		DNS:    &fakeResolver{},
		Static: capturedial.StaticCreds{Username: "lab", Password: "lab"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if built.Engine == nil {
		t.Error("Build returned no engine")
	}
	if built.Store == nil || built.Store.Root() != p.StorePath {
		t.Errorf("store root = %v, want %s", built.Store, p.StorePath)
	}
	if _, err := os.Stat(p.StorePath); err != nil {
		t.Errorf("the store directory was not created: %v", err)
	}
	if len(built.Specs) != 2 {
		t.Errorf("got %d specs, want 2", len(built.Specs))
	}
	if len(built.Devices) != 2 {
		t.Errorf("got %d devices, want 2", len(built.Devices))
	}
	if built.Close == nil {
		t.Error("Close is nil; a caller deferring it panics")
	}
	// No vault means no binding store, which means capture stops
	// contributing to identity. Worth knowing rather than assuming.
	if built.Bindings != nil {
		t.Error("a vault-less build produced a binding store")
	}
}

// A list that parses to nothing must not build a run that visits nothing and
// reports success. A commented-out file is the realistic way to get here.
func TestADeviceListThatResolvesToNothingIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.txt")
	if err := os.WriteFile(path, []byte("# all decommissioned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := capturerun.Defaults()
	p.DeviceFile = path
	p.StorePath = filepath.Join(dir, "captures")
	// Credentials supplied deliberately: without them Build refuses earlier,
	// for a different reason, and this test would pass without ever reaching
	// the empty-list check it is named after.
	opts := capturedial.Options{
		DNS:    &fakeResolver{},
		Static: capturedial.StaticCreds{Username: "lab", Password: "lab"},
	}
	_, err := capturedial.Build(p, opts)
	if err == nil {
		t.Error("Build produced a run with nothing in it")
	} else if !strings.Contains(err.Error(), "no devices") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// Nothing to authenticate with must be caught before the fleet is dialed.
// dial.Static does not enable agent auth, so with no vault, no key and no
// password every device fails the handshake with an SSH-layer message that
// names the symptom and not the cause — which is a whole lab run spent
// learning something Build already knew.
func TestARunWithNoCredentialsIsRefusedUpFront(t *testing.T) {
	p := validParams(t)
	_, err := capturedial.Build(p, capturedial.Options{DNS: &fakeResolver{}})
	if err == nil {
		t.Fatal("Build accepted a run with no credential source at all")
	}
	for _, want := range []string{"vault", "key", "password"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not offer %s as a way out:\n%v", want, err)
		}
	}

	// A key alone is enough, and so is a password alone.
	for _, opts := range []capturedial.Options{
		{DNS: &fakeResolver{}, Static: capturedial.StaticCreds{Username: "lab", KeyPath: "/dev/null"}},
		{DNS: &fakeResolver{}, Static: capturedial.StaticCreds{Username: "lab", Password: "lab"}},
	} {
		if _, err := capturedial.Build(validParams(t), opts); err != nil {
			t.Errorf("Build refused a run that had a credential: %v", err)
		}
	}
}

// The guard.
//
// A parameter that nothing consumes is invisible: no error, no warning, just a
// run that quietly ignores what someone typed. That already happened once on
// the crawl side — TrustUnidirectional existed in Params for weeks with
// nothing reading it, and the symptom was a map with fewer links and no
// indication which ones. crawldial answered it with a reflection walk over
// topo.Options; this is the same idea aimed at the other end, since Build is
// where a Params field goes to be forgotten.
//
// Adding a field to Params without wiring it here fails this test. If a field
// genuinely belongs somewhere else, add it below with the reason.
func TestEveryCaptureParameterIsConsumedByBuild(t *testing.T) {
	// Fields Build never touches directly, and where they are handled
	// instead. Anything not listed must appear as p.<Field> in this
	// package.
	elsewhere := map[string]string{
		"Devices":    "read by Params.ResolveTargetsWith, via Devices()",
		"DeviceFile": "read by Params.Targets, via Devices()",
	}

	used := paramSelectors(t)
	typ := reflect.TypeOf(capturerun.Params{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if why, ok := elsewhere[name]; ok {
			if used[name] {
				t.Errorf("Params.%s is listed as handled elsewhere (%s) "+
					"but Build reads it directly; remove it from the list", name, why)
			}
			continue
		}
		if !used[name] {
			t.Errorf("Params.%s is never read in internal/capturedial. Either wire "+
				"it into Build or record where it is handled instead — a "+
				"parameter nothing consumes is a setting that silently does nothing.",
				name)
		}
	}
}

// paramSelectors returns every field selected off an identifier named p in this
// package's non-test source.
//
// Reads the files one at a time rather than through parser.ParseDir, which is
// deprecated along with ast.Package — a test that makes an editor light up
// costs more attention than the check is worth.
func paramSelectors(t *testing.T) map[string]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the package source")
	}
	dir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "p" {
				out[sel.Sel.Name] = true
			}
			return true
		})
	}
	if scanned == 0 || len(out) == 0 {
		t.Fatalf("scanned %d source file(s) and found %d parameter reads, so this "+
			"guard would pass no matter what; fix the scan before trusting it",
			scanned, len(out))
	}
	return out
}
