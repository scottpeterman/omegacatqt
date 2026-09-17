// internal/sessions/tree_test.go
package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ssh(name, host string) Node {
	return Node{Name: name, Transport: TransportSSH, Host: host}.Normalize()
}

func labTree() Tree {
	t := Tree{}
	_ = t.Add("Lab", ssh("eng-spine-1", "172.16.2.5"))
	_ = t.Add("Lab", ssh("eng-spine-2", "172.16.2.6"))
	_ = t.Add("Core", ssh("wan-core-1", "172.16.1.2"))
	return t
}

// ── file shape ───────────────────────────────────────────────────────

func TestTreeRoundTrips(t *testing.T) {
	in := labTree()
	data, err := MarshalTree(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnmarshalTree(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Folders) != 2 || len(out.Folders[0].Sessions) != 2 {
		t.Fatalf("got %d folders, first has %d", len(out.Folders), len(out.Folders[0].Sessions))
	}
	if out.Version != FileVersion {
		t.Errorf("version = %d, want %d", out.Version, FileVersion)
	}
	if out.Folders[0].Sessions[0].Host != "172.16.2.5" {
		t.Errorf("host did not survive: %q", out.Folders[0].Sessions[0].Host)
	}
}

func TestAnEmptyFileIsAnEmptyTreeNotAnError(t *testing.T) {
	for _, in := range []string{"", "   \n", "# just a comment\n"} {
		got, err := UnmarshalTree([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(got.Folders) != 0 {
			t.Errorf("%q: got %d folders", in, len(got.Folders))
		}
	}
}

func TestABareListOfFoldersLoads(t *testing.T) {
	// The older terminal's outer shape, straight from its README.
	got, err := UnmarshalTree([]byte(`
- folder_name: Production
  sessions:
    - name: web-server-01
      transport: ssh
      host: 10.0.1.10
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Folders) != 1 || got.Folders[0].Name != "Production" {
		t.Fatalf("got %+v", got.Folders)
	}
	if len(got.Folders[0].Sessions) != 1 {
		t.Fatalf("got %d sessions", len(got.Folders[0].Sessions))
	}
}

func TestGarbageIsAnError(t *testing.T) {
	if _, err := UnmarshalTree([]byte("\tthis: is: not: yaml\n  - at all")); err == nil {
		t.Fatal("want an error")
	}
}

func TestLoadingAMissingFileGivesAnEmptyTree(t *testing.T) {
	got, err := LoadFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("a first run has no file yet: %v", err)
	}
	if len(got.Folders) != 0 {
		t.Errorf("got %d folders", len(got.Folders))
	}
}

func TestSaveThenLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "sessions.yaml")
	if err := SaveFile(path, labTree()); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes()) != 3 {
		t.Fatalf("got %d nodes", len(got.Nodes()))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
	// The temp file must not be left behind next to the real one.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".sessions-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSavingCarriesNoSecrets(t *testing.T) {
	tr := Tree{}
	n := ssh("box", "10.0.0.1")
	n.Password = "hunter2"
	n.KeyPassphrase = "opensesame"
	_ = tr.Add("Lab", n)

	data, err := MarshalTree(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "opensesame"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("%q reached the file:\n%s", secret, data)
		}
	}
}

// ── editing ──────────────────────────────────────────────────────────

func TestFolderNamesAreCaseInsensitive(t *testing.T) {
	tr := labTree()
	if err := tr.AddFolder("lab"); err == nil {
		t.Fatal("want a duplicate-folder error")
	}
	if i := tr.FolderIndex("LAB"); i != 0 {
		t.Errorf("FolderIndex(LAB) = %d, want 0", i)
	}
}

func TestRenameKeepsPositionAndContents(t *testing.T) {
	tr := labTree()
	if err := tr.RenameFolder("Lab", "Engineering"); err != nil {
		t.Fatal(err)
	}
	if tr.Folders[0].Name != "Engineering" || len(tr.Folders[0].Sessions) != 2 {
		t.Fatalf("got %+v", tr.Folders[0])
	}
	if err := tr.RenameFolder("Engineering", "Core"); err == nil {
		t.Fatal("want a collision error")
	}
}

func TestRemovingAFullFolderNeedsForce(t *testing.T) {
	tr := labTree()
	if err := tr.RemoveFolder("Lab", false); err == nil {
		t.Fatal("want a refusal")
	}
	if err := tr.RemoveFolder("Lab", true); err != nil {
		t.Fatal(err)
	}
	if len(tr.Folders) != 1 {
		t.Fatalf("got %d folders", len(tr.Folders))
	}
}

func TestReplaceKeepsItsPlaceInTheFolder(t *testing.T) {
	tr := labTree()
	edited := ssh("eng-spine-1", "172.16.2.55")
	if err := tr.Replace("Lab", "eng-spine-1", edited); err != nil {
		t.Fatal(err)
	}
	if got := tr.Folders[0].Sessions[0]; got.Host != "172.16.2.55" {
		t.Fatalf("index 0 is %+v", got)
	}
	if err := tr.Replace("Lab", "eng-spine-1", ssh("eng-spine-2", "10.0.0.9")); err == nil {
		t.Fatal("want a name-collision error")
	}
}

func TestAFailedMoveLeavesTheSessionWhereItWas(t *testing.T) {
	tr := labTree()
	// Core already has wan-core-1; moving a same-named session in must fail
	// without deleting the original.
	_ = tr.Add("Lab", ssh("wan-core-1", "10.9.9.9"))
	if err := tr.Move("Lab", "wan-core-1", "Core"); err == nil {
		t.Fatal("want a collision error")
	}
	if tr.Folders[0].SessionIndex("wan-core-1") < 0 {
		t.Fatal("the session was removed from Lab despite the failure")
	}
}

func TestMoveRelocates(t *testing.T) {
	tr := labTree()
	if err := tr.Move("Lab", "eng-spine-2", "Core"); err != nil {
		t.Fatal(err)
	}
	if tr.Folders[0].SessionIndex("eng-spine-2") >= 0 {
		t.Error("still in Lab")
	}
	if tr.Folders[1].SessionIndex("eng-spine-2") < 0 {
		t.Error("not in Core")
	}
}

// ── import ───────────────────────────────────────────────────────────

func TestImportSkipsWhatIsAlreadyThere(t *testing.T) {
	tr := labTree()
	res := tr.Import("Crawl 2026-08-02", []Node{
		ssh("eng-spine-1", "172.16.2.5"), // same address, different folder
		ssh("eng-leaf-1", "172.16.11.41"),
	})
	if res.Added != 1 {
		t.Errorf("Added = %d, want 1", res.Added)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "eng-spine-1" {
		t.Errorf("Skipped = %v", res.Skipped)
	}
	if len(tr.Nodes()) != 4 {
		t.Errorf("tree has %d nodes, want 4", len(tr.Nodes()))
	}
}

func TestImportIsSafeToRunTwice(t *testing.T) {
	tr := labTree()
	nodes := []Node{ssh("new-1", "10.0.0.1"), ssh("new-2", "10.0.0.2")}
	first := tr.Import("Import", nodes)
	second := tr.Import("Import", nodes)
	if first.Added != 2 || second.Added != 0 {
		t.Fatalf("first added %d, second added %d", first.Added, second.Added)
	}
	if len(second.Skipped) != 2 {
		t.Errorf("Skipped = %v", second.Skipped)
	}
}

func TestSkippingDoesNotDisturbAHandEditedNode(t *testing.T) {
	tr := Tree{}
	edited := ssh("core router (do not touch)", "172.16.1.2")
	edited.Notes = "console cable is behind the rack"
	_ = tr.Add("Mine", edited)

	tr.Import("Crawl", []Node{ssh("wan-core-1", "172.16.1.2")})

	got := tr.Folders[0].Sessions[0]
	if got.Name != "core router (do not touch)" || got.Notes == "" {
		t.Fatalf("the import overwrote a hand-edited node: %+v", got)
	}
}

func TestADifferentPortIsADifferentDevice(t *testing.T) {
	tr := Tree{}
	_ = tr.Add("Lab", ssh("console-a", "10.0.0.1"))
	n := ssh("console-b", "10.0.0.1")
	n.Port = 2201
	res := tr.Import("Lab", []Node{n.Normalize()})
	if res.Added != 1 {
		t.Fatalf("a container console on :2201 is not the host on :22 (%+v)", res)
	}
}

func TestANameClashIsRenamedNotDropped(t *testing.T) {
	tr := Tree{}
	_ = tr.Add("Import", ssh("switch", "10.0.0.1"))
	res := tr.Import("Import", []Node{ssh("switch", "10.0.0.2")})
	if res.Added != 1 {
		t.Fatalf("a new device was dropped over a name: %+v", res)
	}
	if len(res.Renamed) != 1 {
		t.Errorf("Renamed = %v", res.Renamed)
	}
	if len(tr.Folders[0].Sessions) != 2 {
		t.Errorf("folder has %d sessions", len(tr.Folders[0].Sessions))
	}
}

func TestANodeWithNoAddressIsRejected(t *testing.T) {
	tr := Tree{}
	res := tr.Import("Import", []Node{{Name: "nowhere", Transport: TransportSSH}})
	if res.Added != 0 || len(res.Rejected) != 1 {
		t.Fatalf("got %+v", res)
	}
}

// ── map.json ─────────────────────────────────────────────────────────

const miniMap = `{
  "eng-spine-1": {
    "node_details": {"ip": "172.16.2.5", "platform": "arista_eos"},
    "peers": {
      "eng-leaf-1": {"ip": "172.16.11.41", "platform": "cisco_ios", "connections": [["Eth3","Gi0/0"]]},
      "some-server": {"ip": "10.5.5.5", "platform": "linux", "connections": [["Eth9","eth0"]]}
    }
  },
  "eng-leaf-1": {
    "node_details": {"ip": "172.16.11.41", "platform": "cisco_ios"},
    "peers": {"eng-spine-1": {"ip": "172.16.2.5", "platform": "arista_eos", "connections": [["Gi0/0","Eth3"]]}}
  }
}`

func TestMapImportTakesCrawledDevicesNotLeaves(t *testing.T) {
	got, err := NodesFromMap([]byte(miniMap), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want the 2 crawled devices: %+v", len(got), got)
	}
	for _, n := range got {
		if n.Name == "some-server" {
			t.Fatal("a leaf was imported")
		}
	}
}

func TestMapImportCanIncludeLeaves(t *testing.T) {
	got, err := NodesFromMap([]byte(miniMap), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d nodes, want 3", len(got))
	}
}

func TestMapImportAddressesByIPAndKeepsTheReportedName(t *testing.T) {
	got, _ := NodesFromMap([]byte(miniMap), false)
	var found bool
	for _, n := range got {
		if n.Name != "eng-spine-1" {
			continue
		}
		found = true
		if n.Host != "172.16.2.5" {
			t.Errorf("host = %q, want the crawled IP", n.Host)
		}
		if n.Transport != TransportSSH {
			t.Errorf("transport = %q", n.Transport)
		}
		if n.DeviceType != "arista_eos" {
			t.Errorf("device_type = %q", n.DeviceType)
		}
	}
	if !found {
		t.Fatal("eng-spine-1 missing")
	}
}

func TestMapImportOrderIsStable(t *testing.T) {
	// Map iteration order is random; two imports of one file must produce the
	// same folder or it looks like something changed.
	first, _ := NodesFromMap([]byte(miniMap), true)
	for i := 0; i < 20; i++ {
		again, _ := NodesFromMap([]byte(miniMap), true)
		for j := range first {
			if first[j].Name != again[j].Name {
				t.Fatalf("run %d differs at %d: %q vs %q", i, j, first[j].Name, again[j].Name)
			}
		}
	}
}

func TestAnEmptyMapIsAnError(t *testing.T) {
	if _, err := NodesFromMap([]byte(`{}`), false); err == nil {
		t.Fatal("want an error")
	}
}

// ── the older terminal's file ────────────────────────────────────────

const tetherFile = `
- folder_name: Production
  sessions:
    - display_name: web-server-01
      host: 10.0.1.10
      port: "22"
      username: admin
      password: hunter2
      auth_type: publickey
      key_path: ~/.ssh/id_rsa
      DeviceType: linux
    - display_name: edge-switch-01
      host: 10.0.1.20
      port: "2201"
      credsid: net-admin
      DeviceType: arista_eos
      terminal_theme: amber-crt

- folder_name: Lab
  sessions:
    - display_name: cisco-router
      host: 172.16.1.1
      port: not-a-port
      username: admin
      auth_type: password
      devicetype: cisco_ios
      Vendor: Cisco
`

func TestTetherImport(t *testing.T) {
	folders, err := ImportTether([]byte(tetherFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 {
		t.Fatalf("got %d folders", len(folders))
	}

	web := folders[0].Sessions[0]
	if web.Name != "web-server-01" || web.Host != "10.0.1.10" || web.Port != 22 {
		t.Errorf("web = %+v", web)
	}
	if web.AuthType != "publickey" || web.KeyPath != "~/.ssh/id_rsa" {
		t.Errorf("auth did not carry: %+v", web)
	}
	if web.DeviceType != "linux" {
		t.Errorf("DeviceType = %q", web.DeviceType)
	}

	edge := folders[0].Sessions[1]
	if edge.Port != 2201 {
		t.Errorf("a string port must parse: %d", edge.Port)
	}
	if edge.Credential != "net-admin" {
		t.Errorf("credsid = %q, want the credential name", edge.Credential)
	}
	if edge.TerminalTheme != "amber-crt" {
		t.Errorf("terminal_theme = %q", edge.TerminalTheme)
	}
}

func TestTetherImportDropsAPassword(t *testing.T) {
	folders, err := ImportTether([]byte(tetherFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		for _, n := range f.Sessions {
			if n.Password != "" {
				t.Fatalf("%q carried a password in from the file", n.Label())
			}
		}
	}
}

func TestTetherImportSurvivesAnUnparseablePort(t *testing.T) {
	folders, _ := ImportTether([]byte(tetherFile))
	rtr := folders[1].Sessions[0]
	if rtr.Port != 22 {
		t.Errorf("port = %d, want the SSH default rather than a refused file", rtr.Port)
	}
}

func TestTetherImportReadsMetadataInEitherCase(t *testing.T) {
	folders, _ := ImportTether([]byte(tetherFile))
	rtr := folders[1].Sessions[0]
	if rtr.DeviceType != "cisco_ios" {
		t.Errorf("lowercase devicetype was lost: %q", rtr.DeviceType)
	}
	if rtr.Vendor != "Cisco" {
		t.Errorf("Vendor = %q", rtr.Vendor)
	}
}

func TestTetherImportRejectsTheNativeShape(t *testing.T) {
	native, _ := MarshalTree(labTree())
	if _, err := ImportTether(native); err == nil {
		t.Fatal("want an error naming the expected shape")
	}
}

func TestImportedTetherFoldersMergeIntoATree(t *testing.T) {
	folders, _ := ImportTether([]byte(tetherFile))
	tr := labTree()
	total := 0
	for _, f := range folders {
		total += tr.Import(f.Name, f.Sessions).Added
	}
	if total != 3 {
		t.Fatalf("added %d, want 3", total)
	}
	// The file's "Lab" folder merges into the tree's existing one rather than
	// making a second folder of the same name.
	if len(tr.Folders) != 3 {
		t.Fatalf("got %d folders, want Lab + Core + Production", len(tr.Folders))
	}
	if got := len(tr.Folders[0].Sessions); got != 3 {
		t.Errorf("Lab has %d sessions, want its 2 plus the imported router", got)
	}
}

// ── what actually reaches the file ───────────────────────────────────

func TestAnSSHNodeDoesNotCarrySerialFramingIntoTheFile(t *testing.T) {
	tr := Tree{}
	_ = tr.Add("Lab", ssh("box", "10.0.0.1"))
	data, err := MarshalTree(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"baud", "data_bits", "parity", "stop_bits", "serial_port", "telnet_crlf"} {
		if strings.Contains(string(data), key+":") {
			t.Errorf("%q reached an SSH session:\n%s", key, data)
		}
	}
}

func TestASerialNodeKeepsItsFramingAndDropsSSH(t *testing.T) {
	tr := Tree{}
	_ = tr.Add("Console", Node{
		Name: "rack-console", Transport: TransportSerial, SerialPort: "/dev/ttyUSB0",
		Username: "leftover", KeyPath: "~/.ssh/id_rsa",
	}.Normalize())
	data, err := MarshalTree(tr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "baud:") || !strings.Contains(string(data), "serial_port:") {
		t.Errorf("serial framing was trimmed off a serial node:\n%s", data)
	}
	for _, key := range []string{"username", "key_path", "host_key_policy"} {
		if strings.Contains(string(data), key+":") {
			t.Errorf("%q reached a serial session:\n%s", key, data)
		}
	}
}

func TestTrimmingIsRestoredOnLoad(t *testing.T) {
	tr := Tree{}
	_ = tr.Add("Lab", ssh("box", "10.0.0.1"))
	data, _ := MarshalTree(tr)
	back, err := UnmarshalTree(data)
	if err != nil {
		t.Fatal(err)
	}
	n := back.Folders[0].Sessions[0]
	if n.Baud != 9600 || n.DataBits != 8 {
		t.Errorf("defaults did not come back: baud=%d bits=%d", n.Baud, n.DataBits)
	}
	if n.Port != 22 {
		t.Errorf("port = %d", n.Port)
	}
}

func TestADeliberateTelnetCRLFFalseStillSurvivesTheFile(t *testing.T) {
	tr := Tree{}
	n := Node{Name: "console-server", Transport: TransportTelnet, Host: "10.0.0.5"}
	n.SetCRLF(false)
	_ = tr.Add("Lab", n.Normalize())

	data, _ := MarshalTree(tr)
	back, err := UnmarshalTree(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Folders[0].Sessions[0].CRLF() {
		t.Fatal("a deliberate false was trimmed away as if it were absent")
	}
}
