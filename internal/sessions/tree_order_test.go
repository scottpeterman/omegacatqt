// internal/sessions/tree_order_test.go
package sessions

import (
	"strings"
	"testing"
)

func orderTree() Tree {
	t := Tree{}
	_ = t.Add("Lab", ssh("eng-leaf-1", "172.16.2.11"))
	_ = t.Add("Lab", ssh("eng-leaf-2", "172.16.2.12"))
	_ = t.Add("Lab", ssh("eng-spine-1", "172.16.2.5"))
	_ = t.Add("Core", ssh("wan-core-1", "172.16.1.2"))
	_ = t.Add("Edge", ssh("edge-rtr-1", "172.16.3.1"))
	return t
}

func folderOrder(tr Tree) []string {
	out := make([]string, 0, len(tr.Folders))
	for _, f := range tr.Folders {
		out = append(out, f.Name)
	}
	return out
}

func sessionOrder(tr Tree, folder string) []string {
	i := tr.FolderIndex(folder)
	if i < 0 {
		return nil
	}
	out := make([]string, 0, len(tr.Folders[i].Sessions))
	for _, n := range tr.Folders[i].Sessions {
		out = append(out, n.Label())
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ── folders ──────────────────────────────────────────────────────────

func TestReorderFolderUpAndDown(t *testing.T) {
	tr := orderTree()
	eq(t, folderOrder(tr), []string{"Lab", "Core", "Edge"})

	if err := tr.ReorderFolder("Edge", -1); err != nil {
		t.Fatal(err)
	}
	eq(t, folderOrder(tr), []string{"Lab", "Edge", "Core"})

	if err := tr.ReorderFolder("Lab", +2); err != nil {
		t.Fatal(err)
	}
	eq(t, folderOrder(tr), []string{"Edge", "Core", "Lab"})
}

// Reordering past an end is a no-op, not an error: the menu entry is present
// on every row including the first and last.
func TestReorderFolderClampsAtEnds(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderFolder("Lab", -1); err != nil {
		t.Fatal(err)
	}
	eq(t, folderOrder(tr), []string{"Lab", "Core", "Edge"})

	if err := tr.ReorderFolder("Edge", +5); err != nil {
		t.Fatal(err)
	}
	eq(t, folderOrder(tr), []string{"Lab", "Core", "Edge"})
}

func TestReorderFolderKeepsContents(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderFolder("Lab", +1); err != nil {
		t.Fatal(err)
	}
	eq(t, sessionOrder(tr, "Lab"), []string{"eng-leaf-1", "eng-leaf-2", "eng-spine-1"})
}

func TestReorderFolderUnknown(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderFolder("Nowhere", +1); err == nil {
		t.Fatal("expected an error for a folder that is not there")
	}
}

// ── sessions ─────────────────────────────────────────────────────────

func TestReorderSession(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderSession("Lab", "eng-spine-1", -2); err != nil {
		t.Fatal(err)
	}
	eq(t, sessionOrder(tr, "Lab"), []string{"eng-spine-1", "eng-leaf-1", "eng-leaf-2"})
}

func TestReorderSessionClamps(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderSession("Lab", "eng-leaf-1", -3); err != nil {
		t.Fatal(err)
	}
	eq(t, sessionOrder(tr, "Lab"), []string{"eng-leaf-1", "eng-leaf-2", "eng-spine-1"})
}

// A session only moves inside its own folder. Nothing in another folder shifts.
func TestReorderSessionDoesNotLeaveFolder(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderSession("Lab", "eng-spine-1", +9); err != nil {
		t.Fatal(err)
	}
	eq(t, sessionOrder(tr, "Lab"), []string{"eng-leaf-1", "eng-leaf-2", "eng-spine-1"})
	eq(t, sessionOrder(tr, "Core"), []string{"wan-core-1"})
}

func TestReorderSessionUnknown(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderSession("Lab", "not-here", +1); err == nil {
		t.Fatal("expected an error for a session that is not there")
	}
}

// ── duplicate ────────────────────────────────────────────────────────

func TestDuplicateLandsAfterOriginal(t *testing.T) {
	tr := orderTree()
	dup, err := tr.Duplicate("Lab", "eng-leaf-1")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Label() != "eng-leaf-1 copy" {
		t.Fatalf("copy is called %q", dup.Label())
	}
	eq(t, sessionOrder(tr, "Lab"),
		[]string{"eng-leaf-1", "eng-leaf-1 copy", "eng-leaf-2", "eng-spine-1"})
}

func TestDuplicateKeepsSettings(t *testing.T) {
	tr := Tree{}
	n := ssh("edge-rtr-1", "172.16.3.1")
	n.Username = "netops"
	n.DeviceType = "cisco_ios"
	n.Notes = "console via oob"
	_ = tr.Add("Edge", n)

	dup, err := tr.Duplicate("Edge", "edge-rtr-1")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Host != "172.16.3.1" || dup.Username != "netops" ||
		dup.DeviceType != "cisco_ios" || dup.Notes != "console via oob" {
		t.Fatalf("copy lost settings: %+v", dup)
	}
}

// Duplicating repeatedly counts up instead of stacking the word.
func TestDuplicateTwiceCountsUp(t *testing.T) {
	tr := orderTree()
	if _, err := tr.Duplicate("Lab", "eng-leaf-1"); err != nil {
		t.Fatal(err)
	}
	dup2, err := tr.Duplicate("Lab", "eng-leaf-1")
	if err != nil {
		t.Fatal(err)
	}
	if dup2.Label() != "eng-leaf-1 copy 2" {
		t.Fatalf("second copy is called %q", dup2.Label())
	}
	dup3, err := tr.Duplicate("Lab", "eng-leaf-1 copy")
	if err != nil {
		t.Fatal(err)
	}
	if dup3.Label() != "eng-leaf-1 copy 3" {
		t.Fatalf("copy of a copy is called %q", dup3.Label())
	}
}

// A host-only session has no Name, so its copy must be given one or both rows
// label as the host and the tree cannot tell them apart.
func TestDuplicateNamesAHostOnlySession(t *testing.T) {
	tr := Tree{}
	_ = tr.Add("Lab", Node{Transport: TransportSSH, Host: "172.16.2.99"}.Normalize())

	dup, err := tr.Duplicate("Lab", "172.16.2.99")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Name == "" {
		t.Fatal("copy of a host-only session has no name")
	}
	if dup.Label() == "172.16.2.99" {
		t.Fatal("copy still labels as the host")
	}
	if dup.Host != "172.16.2.99" {
		t.Fatalf("copy lost the host: %q", dup.Host)
	}
}

func TestDuplicateUnknown(t *testing.T) {
	tr := orderTree()
	if _, err := tr.Duplicate("Lab", "not-here"); err == nil {
		t.Fatal("expected an error for a session that is not there")
	}
}

// ── the point of all of it ───────────────────────────────────────────

// A hand-chosen order is only worth choosing if it survives being written and
// read back. Nothing on the save path sorts; this is the test that says so.
func TestOrderSurvivesRoundTrip(t *testing.T) {
	tr := orderTree()
	if err := tr.ReorderFolder("Edge", -2); err != nil {
		t.Fatal(err)
	}
	if err := tr.ReorderSession("Lab", "eng-spine-1", -2); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Duplicate("Lab", "eng-leaf-1"); err != nil {
		t.Fatal(err)
	}

	data, err := MarshalTree(tr)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalTree(data)
	if err != nil {
		t.Fatal(err)
	}

	eq(t, folderOrder(back), []string{"Edge", "Lab", "Core"})
	eq(t, sessionOrder(back, "Lab"),
		[]string{"eng-spine-1", "eng-leaf-1", "eng-leaf-1 copy", "eng-leaf-2"})
}
