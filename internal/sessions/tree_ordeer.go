// internal/sessions/tree_order.go
//
// Hand ordering, and copying a session.
//
// The tree is the user's own list — folders in the order somebody put them in,
// sessions in the order somebody put them in. Nothing on the save path sorts:
// normalized() rebuilds folders and sessions in the order it finds them, which
// is what makes a chosen order worth choosing. These three operations are the
// ones that let that order be chosen from the application rather than by
// hand-editing the file.
//
// # Why a delta and not an index
//
// Every caller is a menu entry that says "Move Up" or "Move Down". Taking a
// target index instead would mean the widget computing a position from a row it
// already identified by name, which is the arithmetic this file exists to keep
// out of the UI. A delta of any size is accepted and clamped, so a future
// "move to top" is -len rather than a new method.
//
// # Why the ends are not an error
//
// The top folder has "Move Up" in its menu, because a menu whose entries appear
// and disappear per row is harder to use than one that occasionally does
// nothing. Reordering past an end is therefore a no-op that reports success —
// the same choice Move already makes when the destination folder is the folder
// the session is in.
package sessions

import (
	"fmt"
	"strconv"
	"strings"
)

// ReorderFolder moves a folder by delta positions, negative being toward the
// top. Moving past either end stops at the end.
func (t *Tree) ReorderFolder(name string, delta int) error {
	i := t.FolderIndex(name)
	if i < 0 {
		return fmt.Errorf("no folder called %q", name)
	}
	j := clampIndex(i+delta, len(t.Folders))
	if j == i {
		return nil
	}
	t.Folders = moveFolder(t.Folders, i, j)
	return nil
}

// ReorderSession moves a session within its folder by delta positions. It does
// not move a session between folders — that is Move, which has to think about
// name collisions in the destination.
func (t *Tree) ReorderSession(folder, name string, delta int) error {
	i := t.FolderIndex(folder)
	if i < 0 {
		return fmt.Errorf("no folder called %q", folder)
	}
	j := t.Folders[i].SessionIndex(name)
	if j < 0 {
		return fmt.Errorf("no session called %q in %q", name, folder)
	}
	k := clampIndex(j+delta, len(t.Folders[i].Sessions))
	if k == j {
		return nil
	}
	t.Folders[i].Sessions = moveNode(t.Folders[i].Sessions, j, k)
	return nil
}

// Duplicate copies a session and inserts the copy directly after the original,
// returning it so a caller can select the new row.
//
// The copy lands next to its original rather than at the end of the folder,
// because the reason to duplicate is almost always "another one of these" — a
// second link to the same site, a second console on the same shelf — and a copy
// that appeared thirty rows down would have to be found before it could be
// edited.
//
// The copy is given an explicit name even when the original had none. A node
// with no Name displays its Host, so two copies of a host-only session would
// both label as that host and collide in the tree; naming the copy is what
// makes it addressable at all.
func (t *Tree) Duplicate(folder, name string) (Node, error) {
	i := t.FolderIndex(folder)
	if i < 0 {
		return Node{}, fmt.Errorf("no folder called %q", folder)
	}
	j := t.Folders[i].SessionIndex(name)
	if j < 0 {
		return Node{}, fmt.Errorf("no session called %q in %q", name, folder)
	}

	dup := t.Folders[i].Sessions[j]
	dup.Name = copyLabel(t.Folders[i], dup.Label())
	dup = dup.Normalize()

	s := t.Folders[i].Sessions
	s = append(s, Node{})
	copy(s[j+2:], s[j+1:])
	s[j+1] = dup
	t.Folders[i].Sessions = s

	return dup, nil
}

// copyLabel picks a free name for a copy of base: "core-1 copy", then
// "core-1 copy 2", and so on. The suffix is a word rather than a repeated
// symbol so that duplicating a duplicate reads as a list instead of growing a
// tail of punctuation.
func copyLabel(f Folder, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "session"
	}
	// Duplicating a copy restarts the count from the original name rather than
	// producing "core-1 copy copy".
	if trimmed, ok := stripCopySuffix(base); ok {
		base = trimmed
	}
	want := base + " copy"
	for n := 2; f.SessionIndex(want) >= 0; n++ {
		want = base + " copy " + strconv.Itoa(n)
	}
	return want
}

// stripCopySuffix removes a trailing " copy" or " copy N" if one is there.
func stripCopySuffix(s string) (string, bool) {
	i := strings.LastIndex(strings.ToLower(s), " copy")
	if i < 0 {
		return s, false
	}
	rest := strings.TrimSpace(s[i+len(" copy"):])
	if rest != "" {
		if _, err := strconv.Atoi(rest); err != nil {
			return s, false
		}
	}
	head := strings.TrimSpace(s[:i])
	if head == "" {
		return s, false
	}
	return head, true
}

// clampIndex keeps a computed position inside a slice of length n.
func clampIndex(i, n int) int {
	if i < 0 {
		return 0
	}
	if i > n-1 {
		return n - 1
	}
	return i
}

// moveFolder and moveNode are the same splice on two element types, written
// twice because this package targets a Go floor without generics in its own
// build tags and a []any round trip would copy every node.
func moveFolder(s []Folder, from, to int) []Folder {
	v := s[from]
	s = append(s[:from], s[from+1:]...)
	s = append(s, Folder{})
	copy(s[to+1:], s[to:])
	s[to] = v
	return s
}

func moveNode(s []Node, from, to int) []Node {
	v := s[from]
	s = append(s[:from], s[from+1:]...)
	s = append(s, Node{})
	copy(s[to+1:], s[to:])
	s[to] = v
	return s
}
