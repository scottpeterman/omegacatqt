// internal/sessions/importfile_test.go
package sessions

import (
	"strings"
	"testing"
)

const nativeTreeFile = `version: 1
folders:
  - folder_name: Lab
    sessions:
      - name: lab-r1
        transport: ssh
        host: 172.16.1.2
`

const tetherListFile = `- folder_name: Lab
  sessions:
    - display_name: lab-r1
      host: 172.16.1.2
      port: "22"
      username: admin
      auth_type: publickey
      DeviceType: cisco_ios
`

const mapJSONFile = `{
  "lab-r1": {
    "node_details": {"ip": "172.16.1.2", "platform": "cisco_ios"},
    "peers": {}
  }
}`

func TestSniffTellsTheThreeFilesApart(t *testing.T) {
	cases := []struct {
		name string
		data string
		want Format
	}{
		{"native", nativeTreeFile, FormatNative},
		{"tether", tetherListFile, FormatTether},
		{"map", mapJSONFile, FormatMap},
		{"empty", "   \n", FormatNative},
		{"garbage", "\tthis: is: not: yaml: at: all\n  - [", FormatUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sniff([]byte(c.data)); got != c.want {
				t.Fatalf("Sniff = %v, want %v", got, c.want)
			}
		})
	}
}

// A native file with no folders yet still has to read as native, or a tree
// saved before anything was added becomes unimportable.
func TestAnEmptyNativeTreeIsStillNative(t *testing.T) {
	if got := Sniff([]byte("version: 1\nfolders: []\n")); got != FormatNative {
		t.Fatalf("Sniff = %v, want FormatNative", got)
	}
}

func TestFoldersFromReadsBothSessionShapes(t *testing.T) {
	for _, c := range []struct {
		name   string
		data   string
		format Format
	}{
		{"native", nativeTreeFile, FormatNative},
		{"tether", tetherListFile, FormatTether},
	} {
		t.Run(c.name, func(t *testing.T) {
			folders, format, err := FoldersFrom([]byte(c.data))
			if err != nil {
				t.Fatalf("FoldersFrom: %v", err)
			}
			if format != c.format {
				t.Fatalf("format = %v, want %v", format, c.format)
			}
			if len(folders) != 1 || len(folders[0].Sessions) != 1 {
				t.Fatalf("got %d folders, want 1 with 1 session", len(folders))
			}
			if got := folders[0].Sessions[0].Host; got != "172.16.1.2" {
				t.Fatalf("host = %q", got)
			}
		})
	}
}

// Picking a map from the session-import menu is the mistake most worth a real
// message, since both files are YAML-ish and live in the same directory.
func TestFoldersFromRefusesAMapAndSaysSo(t *testing.T) {
	_, format, err := FoldersFrom([]byte(mapJSONFile))
	if err == nil {
		t.Fatal("FoldersFrom accepted a map")
	}
	if format != FormatMap {
		t.Fatalf("format = %v, want FormatMap", format)
	}
	if !strings.Contains(err.Error(), "topology map") {
		t.Fatalf("error does not name the problem: %v", err)
	}
}

func TestImportFoldersKeepsTheSourceStructure(t *testing.T) {
	src := []Folder{
		{Name: "Core", Sessions: []Node{
			{Name: "lab-r1", Transport: TransportSSH, Host: "172.16.1.2"},
			{Name: "lab-r2", Transport: TransportSSH, Host: "172.16.1.3"},
		}},
		{Name: "Edge", Sessions: []Node{
			{Name: "lab-e1", Transport: TransportSSH, Host: "172.16.2.5"},
		}},
	}

	var tr Tree
	s := tr.ImportFolders(src)

	if s.Added != 3 {
		t.Fatalf("Added = %d, want 3", s.Added)
	}
	if len(tr.Folders) != 2 {
		t.Fatalf("got %d folders, want 2", len(tr.Folders))
	}
	if len(s.Created) != 2 {
		t.Fatalf("Created = %v, want both folders", s.Created)
	}
	if tr.FolderIndex("Core") < 0 || tr.FolderIndex("Edge") < 0 {
		t.Fatalf("folder names not preserved: %+v", tr.Folders)
	}
}

// The whole reason import skips by address: importing the same file twice is a
// normal thing to do, and it must not double the tree.
func TestImportFoldersIsSafeToRunTwice(t *testing.T) {
	src := []Folder{{Name: "Core", Sessions: []Node{
		{Name: "lab-r1", Transport: TransportSSH, Host: "172.16.1.2"},
	}}}

	var tr Tree
	tr.ImportFolders(src)
	s := tr.ImportFolders(src)

	if s.Added != 0 {
		t.Fatalf("second import added %d, want 0", s.Added)
	}
	if s.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", s.Skipped)
	}
	if len(s.Created) != 0 {
		t.Fatalf("second import claimed to create %v", s.Created)
	}
	if n := len(tr.Nodes()); n != 1 {
		t.Fatalf("tree has %d sessions after two imports, want 1", n)
	}
}

// An empty folder is structure somebody made on purpose. Dropping it is a loss
// nothing else reports.
func TestAnEmptySourceFolderIsStillCreated(t *testing.T) {
	var tr Tree
	s := tr.ImportFolders([]Folder{{Name: "Staging"}})

	if tr.FolderIndex("Staging") < 0 {
		t.Fatal("empty folder was dropped")
	}
	if len(s.Created) != 1 {
		t.Fatalf("Created = %v, want Staging", s.Created)
	}
}

// A session with nothing to dial cannot become a session, and the person who
// picked the file is the only one who can do anything about it.
func TestImportFoldersReportsWhatItCouldNotUse(t *testing.T) {
	var tr Tree
	s := tr.ImportFolders([]Folder{{Name: "Core", Sessions: []Node{
		{Name: "lab-r1", Transport: TransportSSH, Host: "172.16.1.2"},
		{Name: "ghost", Transport: TransportSSH},
	}}})

	if s.Added != 1 {
		t.Fatalf("Added = %d, want 1", s.Added)
	}
	if len(s.Rejected) != 1 || s.Rejected[0] != "ghost" {
		t.Fatalf("Rejected = %v, want [ghost]", s.Rejected)
	}
	if !strings.Contains(s.Describe(), "ghost") {
		t.Fatalf("summary hides the rejection: %q", s.Describe())
	}
}

func TestDescribeSaysNothingAboutEmptyCategories(t *testing.T) {
	s := ImportSummary{Folders: []string{"Core"}, Added: 2}
	got := s.Describe()

	if !strings.Contains(got, "2 sessions") || !strings.Contains(got, "Core") {
		t.Fatalf("summary = %q", got)
	}
	for _, unwanted := range []string{"Renamed", "Already", "New folder", "connect to"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("summary mentions an empty category %q: %q", unwanted, got)
		}
	}
}

func TestDescribeCallsACleanReimportWhatItIs(t *testing.T) {
	s := ImportSummary{Skipped: 13}
	if got := s.Describe(); !strings.Contains(got, "already in the tree") {
		t.Fatalf("summary = %q", got)
	}
}

func TestNameListStopsBeforeItBecomesUnreadable(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	got := nameList(names)
	if !strings.Contains(got, "2 more") {
		t.Fatalf("nameList = %q", got)
	}
	if strings.Contains(got, "h") {
		t.Fatalf("nameList listed everything: %q", got)
	}
}
