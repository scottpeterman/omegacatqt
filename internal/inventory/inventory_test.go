package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottpeterman/omegacatqt/internal/capturedial"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
)

// Two crawled devices and one leaf, shaped like omegamaps' map.json.
const labMap = `{
  "eng-spine-1": {
    "node_details": {"ip": "172.16.2.5", "platform": "arista_eos"},
    "peers": {
      "eng-leaf-1": {"ip": "172.16.11.41", "platform": "cisco_ios", "connections": [["Eth3","Gi0/0"]]},
      "lab-server": {"ip": "172.16.99.9", "platform": "linux", "connections": [["Eth9","eth0"]]}
    }
  },
  "eng-leaf-1": {
    "node_details": {"ip": "172.16.11.41", "platform": "cisco_ios"},
    "peers": {"eng-spine-1": {"ip": "172.16.2.5", "platform": "arista_eos", "connections": [["Gi0/0","Eth3"]]}}
  }
}`

func writeMap(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMissingInventoryIsEmpty(t *testing.T) {
	v, err := Load(filepath.Join(t.TempDir(), "nope", FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Folders) != 0 {
		t.Fatalf("want no folders, got %+v", v.Folders)
	}
}

func TestImportMapAddsCrawledDevicesNotLeaves(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, "home", ".omegacat", FileName) // directory does not exist yet
	mp := writeMap(t, dir, "crawl-lab/map.json", labMap)

	res, err := ImportMap(inv, mp, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Folder != "crawl-lab" || !res.Created || res.Added != 2 || res.Skipped != 0 {
		t.Fatalf("unexpected result %+v", res)
	}

	v, err := Load(inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Folders) != 1 || len(v.Folders[0].Sessions) != 2 {
		t.Fatalf("want one folder with 2 sessions, got %+v", v.Folders)
	}
	got := map[string]Session{}
	for _, s := range v.Folders[0].Sessions {
		got[s.Name] = s
	}
	if s := got["eng-spine-1"]; s.Host != "172.16.2.5" || s.DeviceType != "arista_eos" || s.Platform != "" || s.Transport != "ssh" {
		t.Fatalf("eng-spine-1 imported as %+v", s)
	}
	if _, ok := got["lab-server"]; ok {
		t.Fatal("a leaf was imported")
	}
	if st, err := os.Stat(inv); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("inventory not written 0600: %v %v", st, err)
	}
}

func TestReimportAddsOnlyNewAddressesAndKeepsHandEdits(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, FileName)
	mp := writeMap(t, dir, "lab.json", labMap)
	if _, err := ImportMap(inv, mp, "Lab"); err != nil {
		t.Fatal(err)
	}

	// Somebody renames a device by hand in the file.
	data, _ := os.ReadFile(inv)
	edited := strings.Replace(string(data), "name: eng-leaf-1", "name: eng-leaf-1-renamed", 1)
	if edited == string(data) {
		t.Fatalf("fixture did not contain the name to edit:\n%s", data)
	}
	if err := os.WriteFile(inv, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	// A later crawl: same two devices plus one new one.
	later := strings.Replace(labMap, `"eng-leaf-1": {
    "node_details"`, `"eng-leaf-2": {
    "node_details": {"ip": "172.16.11.42", "platform": "cisco_ios"},
    "peers": {}
  },
  "eng-leaf-1": {
    "node_details"`, 1)
	mp2 := writeMap(t, dir, "lab2.json", later)
	res, err := ImportMap(inv, mp2, "Lab")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Skipped != 2 || res.Created {
		t.Fatalf("re-import: want 1 added, 2 skipped, folder not created; got %+v", res)
	}
	v, _ := Load(inv)
	names := []string{}
	for _, s := range v.Folders[0].Sessions {
		names = append(names, s.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "eng-leaf-1-renamed") || !strings.Contains(joined, "eng-leaf-2") ||
		strings.Contains(joined, "eng-leaf-1,") {
		t.Fatalf("hand edit lost or new device missing: %s", joined)
	}

	// The same file a third time changes nothing and does not rewrite it.
	before, _ := os.Stat(inv)
	res, err = ImportMap(inv, mp2, "Lab")
	if err != nil || res.Added != 0 || res.Skipped != 3 {
		t.Fatalf("third import: %+v %v", res, err)
	}
	after, _ := os.Stat(inv)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an import that added nothing rewrote the file")
	}
}

func TestImportRefusesWhatIsNotAMap(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, FileName)
	sessionFile := writeMap(t, dir, "sessions.json", "version: 1\nfolders:\n  - name: Lab\n    sessions: []\n")
	if _, err := ImportMap(inv, sessionFile, ""); err == nil || !strings.Contains(err.Error(), "not an omegamaps map.json") {
		t.Fatalf("want a refusal naming the format, got %v", err)
	}
	if _, err := os.Stat(inv); !os.IsNotExist(err) {
		t.Fatal("a refused import wrote the inventory")
	}
}

func TestFolderForMap(t *testing.T) {
	for in, want := range map[string]string{
		"/x/crawl-lab/map.json": "crawl-lab",
		"/x/y/MAP.JSON":         "y",
		"/x/y/site-a.json":      "site-a",
		"map.json":              "map",
	} {
		if got := FolderForMap(in); got != want {
			t.Errorf("FolderForMap(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoveFolder(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, FileName)
	mp := writeMap(t, dir, "lab.json", labMap)
	if _, err := ImportMap(inv, mp, "Lab"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFolder(inv, "Lab"); err != nil {
		t.Fatal(err)
	}
	v, _ := Load(inv)
	if len(v.Folders) != 0 {
		t.Fatalf("folder still there: %+v", v.Folders)
	}
	if err := RemoveFolder(inv, "Lab"); err == nil {
		t.Fatal("removing a missing folder succeeded")
	}
}

// The capture side: the hosts a view sends as match patterns select exactly
// those sessions, and each device carries the map's name as its identity with
// the address it is dialled on.
func TestImportedInventorySelectsByHostForCapture(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, FileName)
	mp := writeMap(t, dir, "lab.json", labMap)
	if _, err := ImportMap(inv, mp, "Lab"); err != nil {
		t.Fatal(err)
	}
	p := capturerun.Defaults()
	p.SessionFile = inv
	p.Match = []string{"172.16.11.41"}
	devs, _, skipped, err := capturedial.SessionDevices(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 || len(devs) != 1 {
		t.Fatalf("want exactly one device, got %+v (skipped %v)", devs, skipped)
	}
	if d := devs[0]; d.Identity != "eng-leaf-1" || d.Target != "172.16.11.41" {
		t.Fatalf("device %+v: want identity eng-leaf-1 dialled at 172.16.11.41", d)
	}
}

// Keys from Load, sent back as session_keys, select exactly the ticked entry
// even when another entry shares its address.
func TestLoadedKeysSelectOneOfTwoForwardedDevices(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, FileName)
	body := `version: 1
folders:
    - folder_name: Forwarded
      sessions:
        - name: lab-r1
          transport: ssh
          host: 127.0.0.1
          port: 2201
        - name: lab-spine-1
          transport: ssh
          host: 127.0.0.1
          port: 2202
`
	if err := os.WriteFile(inv, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := Load(inv)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	for _, s := range v.Folders[0].Sessions {
		if s.Name == "lab-spine-1" {
			key = s.Key
		}
	}
	if key != "ssh:127.0.0.1:2202" {
		t.Fatalf("lab-spine-1 key = %q", key)
	}
	p := capturerun.Defaults()
	p.SessionFile = inv
	p.SessionKeys = []string{key}
	devs, _, _, err := capturedial.SessionDevices(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Identity != "lab-spine-1" || devs[0].Target != "127.0.0.1:2202" {
		t.Fatalf("want only lab-spine-1 at 127.0.0.1:2202, got %+v", devs)
	}
}

// Change a device's address by hand, re-import the crawl that still has the old
// one: the device is recognised by name in its folder, not added again.
func TestReimportAfterAnAddressEditDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, FileName)
	mp := writeMap(t, dir, "lab.json", labMap)
	if _, err := ImportMap(inv, mp, "Lab"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(inv)
	edited := strings.Replace(string(data), "host: 172.16.11.41", "host: 172.16.11.141", 1)
	if edited == string(data) {
		t.Fatalf("fixture lacks the address to edit:\n%s", data)
	}
	if err := os.WriteFile(inv, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := ImportMap(inv, mp, "Lab")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Skipped != 2 || len(res.Renamed) != 0 {
		t.Fatalf("want nothing added or renamed, 2 recognised; got %+v", res)
	}
	v, _ := Load(inv)
	if n := len(v.Folders[0].Sessions); n != 2 {
		t.Fatalf("%d sessions after re-import, want 2", n)
	}
	for _, s := range v.Folders[0].Sessions {
		if s.Name == "eng-leaf-1" && s.Host != "172.16.11.141" {
			t.Errorf("the hand-edited address was replaced: %+v", s)
		}
	}
}

// A later crawl with a different platform guess refreshes device_type and
// touches nothing a person owns.
func TestReimportRefreshesTheHintAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	inv := filepath.Join(dir, FileName)
	mp := writeMap(t, dir, "lab.json", labMap)
	if _, err := ImportMap(inv, mp, "Lab"); err != nil {
		t.Fatal(err)
	}
	// A person sets platform, legacy and credential on eng-leaf-1.
	data, _ := os.ReadFile(inv)
	edited := strings.Replace(string(data), "host: 172.16.11.41\n",
		"host: 172.16.11.41\n          platform: cisco_iosxe\n          legacy_algorithms: true\n          credential: lab-tacacs\n", 1)
	if edited == string(data) {
		t.Fatalf("fixture lacks the host line:\n%s", data)
	}
	if err := os.WriteFile(inv, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := Load(inv); err != nil || v.Folders[0].Sessions[0].Name != "eng-leaf-1" || v.Folders[0].Sessions[0].Platform != "cisco_iosxe" {
		t.Fatalf("hand edit did not load: %+v %v", v, err)
	}

	later := strings.ReplaceAll(labMap, `"ip": "172.16.11.41", "platform": "cisco_ios"`, `"ip": "172.16.11.41", "platform": "cisco_nxos"`)
	mp2 := writeMap(t, dir, "lab2.json", later)
	res, err := ImportMap(inv, mp2, "Lab")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Refreshed != 1 {
		t.Fatalf("want 0 added, 1 refreshed; got %+v", res)
	}
	v, _ := Load(inv)
	var leaf Session
	for _, s := range v.Folders[0].Sessions {
		if s.Name == "eng-leaf-1" {
			leaf = s
		}
	}
	if leaf.DeviceType != "cisco_nxos" {
		t.Errorf("device_type %q, want the new guess", leaf.DeviceType)
	}
	if leaf.Platform != "cisco_iosxe" || !leaf.Legacy || leaf.Credential != "lab-tacacs" {
		t.Errorf("an import changed what a person set: %+v", leaf)
	}
}

// What a session carries reaches the capture device: the authority, the hint,
// and legacy.
func TestSessionPlatformHintAndLegacyReachTheCaptureDevice(t *testing.T) {
	inv := filepath.Join(t.TempDir(), FileName)
	body := `version: 1
folders:
    - folder_name: Lab
      sessions:
        - name: old-sw
          transport: ssh
          host: 172.16.9.9
          platform: cisco_ios
          device_type: hp_comware
          legacy_algorithms: true
        - name: new-sw
          transport: ssh
          host: 172.16.9.10
          device_type: arista_eos
`
	if err := os.WriteFile(inv, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p := capturerun.Defaults()
	p.SessionFile = inv
	p.Match = []string{"*"}
	devs, _, _, err := capturedial.SessionDevices(p, nil)
	if err != nil || len(devs) != 2 {
		t.Fatalf("%+v %v", devs, err)
	}
	old, fresh := devs[0], devs[1]
	if old.Platform != "cisco_ios" || old.PlatformHint != "hp_comware" || !old.Legacy {
		t.Errorf("old-sw: %+v", old)
	}
	if fresh.Platform != "" || fresh.PlatformHint != "arista_eos" || fresh.Legacy {
		t.Errorf("new-sw: %+v", fresh)
	}
}
