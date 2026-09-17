package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const editFixture = `version: 1
folders:
    - folder_name: Lab
      sessions:
        - name: eng-leaf-1
          transport: ssh
          host: 172.16.11.41
          device_type: cisco_ios
          username: operator
        - name: eng-leaf-2
          transport: ssh
          host: 172.16.11.42
        - name: eng-spine-1
          transport: ssh
          host: 172.16.2.5
    - folder_name: Core
      sessions:
        - name: wan-core-1
          transport: ssh
          host: 172.16.1.2
`

func editInv(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(editFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func find(t *testing.T, path, name string) (Session, string) {
	t.Helper()
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range v.Folders {
		for _, s := range f.Sessions {
			if s.Name == name {
				return s, f.Name
			}
		}
	}
	return Session{}, ""
}

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }

func TestSaveEditsOnlyTheEditorsFieldsAndReportsTheNewKey(t *testing.T) {
	inv := editInv(t)
	res, err := Apply(inv, []Op{{Op: "save", Key: "ssh:172.16.11.41:22", Session: &Fields{
		Name: "eng-leaf-1", Host: "172.16.11.141", Port: 2222, Platform: "cisco_iosxe", Legacy: true, Credential: "lab-tacacs",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Keys["ssh:172.16.11.41:22"] != "ssh:172.16.11.141:2222" || len(res.Keys) != 1 {
		t.Fatalf("key map %v", res.Keys)
	}
	s, _ := find(t, inv, "eng-leaf-1")
	if s.Host != "172.16.11.141" || s.Port != 2222 || s.Platform != "cisco_iosxe" || !s.Legacy || s.Credential != "lab-tacacs" {
		t.Errorf("edit not applied: %+v", s)
	}
	if s.DeviceType != "cisco_ios" {
		t.Errorf("device_type %q: an edit must leave the import's guess alone", s.DeviceType)
	}
	data, _ := os.ReadFile(inv)
	if !strings.Contains(string(data), "username: operator") {
		t.Error("a field the editor does not own was dropped")
	}
}

func TestAddCreatesADeviceAndRefusesAnOccupiedAddress(t *testing.T) {
	inv := editInv(t)
	res, err := Apply(inv, []Op{{Op: "save", Folder: "Core", Session: &Fields{Name: "wan-core-2", Host: "172.16.1.3"}}})
	if err != nil || len(res.Added) != 1 || res.Added[0] != "ssh:172.16.1.3:22" {
		t.Fatalf("add: %+v %v", res, err)
	}
	if _, folder := find(t, inv, "wan-core-2"); folder != "Core" {
		t.Errorf("added to %q", folder)
	}
	before, _ := os.ReadFile(inv)
	_, err = Apply(inv, []Op{{Op: "save", Folder: "Lab", Session: &Fields{Name: "dup", Host: "172.16.2.5"}}})
	if err == nil || !strings.Contains(err.Error(), "already at ssh:172.16.2.5:22") {
		t.Fatalf("want a refusal naming the key, got %v", err)
	}
	after, _ := os.ReadFile(inv)
	if string(before) != string(after) {
		t.Error("a refused edit wrote the file")
	}
}

func TestAnUnknownPlatformIsRefused(t *testing.T) {
	inv := editInv(t)
	_, err := Apply(inv, []Op{{Op: "patch", Keys: []string{"ssh:172.16.2.5:22"}, Platform: sp("cisco_ioss")}})
	if err == nil || !strings.Contains(err.Error(), `"cisco_ioss"`) {
		t.Fatalf("got %v", err)
	}
}

// Many devices, one call: a failure part-way through changes nothing.
func TestABulkEditLandsWholeOrNotAtAll(t *testing.T) {
	inv := editInv(t)
	keys := []string{"ssh:172.16.11.41:22", "ssh:172.16.11.42:22"}
	if _, err := Apply(inv, []Op{{Op: "patch", Keys: keys, Credential: sp("lab-tacacs"), Legacy: bp(true)}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"eng-leaf-1", "eng-leaf-2"} {
		if s, _ := find(t, inv, name); s.Credential != "lab-tacacs" || !s.Legacy || s.Platform != "" {
			t.Errorf("%s after patch: %+v", name, s)
		}
	}

	before, _ := os.ReadFile(inv)
	_, err := Apply(inv, []Op{{Op: "patch", Keys: append(keys, "ssh:10.9.9.9:22"), Credential: sp("other")}})
	if err == nil {
		t.Fatal("a patch naming a missing session succeeded")
	}
	after, _ := os.ReadFile(inv)
	if string(before) != string(after) {
		t.Error("a failed bulk edit changed some devices")
	}
}

func TestMoveAndFolderOperations(t *testing.T) {
	inv := editInv(t)
	res, err := Apply(inv, []Op{
		{Op: "add_folder", Name: "Spines"},
		{Op: "move", Keys: []string{"ssh:172.16.2.5:22"}, To: "Spines"},
		{Op: "rename_folder", Folder: "Lab", To: "Leaves"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keys) != 0 {
		t.Errorf("a move or rename changed keys: %v", res.Keys)
	}
	if _, f := find(t, inv, "eng-spine-1"); f != "Spines" {
		t.Errorf("eng-spine-1 in %q", f)
	}
	if _, f := find(t, inv, "eng-leaf-1"); f != "Leaves" {
		t.Errorf("eng-leaf-1 in %q", f)
	}

	res, err = Apply(inv, []Op{{Op: "remove_folder", Folder: "Leaves"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Keys["ssh:172.16.11.41:22"] != "" || len(res.Keys) != 2 {
		t.Errorf("removed folder's keys not reported deleted: %v", res.Keys)
	}
	if _, ok := res.Keys["ssh:172.16.11.41:22"]; !ok {
		t.Error("deleted key absent from the map")
	}
}

// A key edited twice in one call reports original -> final.
func TestKeysEditedTwiceMapFromTheOriginal(t *testing.T) {
	inv := editInv(t)
	res, err := Apply(inv, []Op{
		{Op: "save", Key: "ssh:172.16.1.2:22", Session: &Fields{Name: "wan-core-1", Host: "172.16.1.20"}},
		{Op: "save", Key: "ssh:172.16.1.20:22", Session: &Fields{Name: "wan-core-1", Host: "172.16.1.200"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keys) != 1 || res.Keys["ssh:172.16.1.2:22"] != "ssh:172.16.1.200:22" {
		t.Errorf("key map %v", res.Keys)
	}
}
