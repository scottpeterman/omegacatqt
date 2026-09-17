// internal/inventory/edit.go
//
// Editing the inventory: one device, many devices, and folders.
//
// # One call, many operations, one write
//
// Apply takes a list of operations, applies them to the file in memory in
// order, and writes only if every one succeeded. A bulk edit of forty devices
// or a move of a multi-selection is one list, so it lands whole or not at all:
// an error half-way through never leaves twenty devices on the new credential
// and twenty on the old one.
//
// # Sessions are addressed by key
//
// A session's key is transport:host:port -- what capture's session_keys
// selects on and what the Run view's ticks are stored as. Keys are kept unique
// across the whole inventory: a save or patch that would give two sessions the
// same key is refused, because two entries with one key are two ticks that
// select each other.
//
// Editing a host or port changes a key. The result maps every original key an
// Apply changed to its new key ("" for a deleted session), so a view holding
// ticks can carry them across.
//
// # What an edit may touch
//
// A save sets name, host, port, platform, legacy and credential and leaves
// every other field of the session -- device_type, username, jump, notes --
// as it was. device_type is the import's guess and stays the import's.
package inventory

import (
	"fmt"
	"strings"

	"github.com/scottpeterman/omegacatqt/internal/netexec"
	"github.com/scottpeterman/omegacatqt/internal/sessions"
)

// Fields are the parts of a session an editor sets.
type Fields struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port,omitempty"`
	Platform   string `json:"platform,omitempty"`
	Legacy     bool   `json:"legacy,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Op is one edit. Which fields matter depends on Op:
//
//	save           Folder, Key ("" adds a session to Folder), Session
//	delete         Keys
//	move           Keys, To (an existing folder)
//	patch          Keys, and any of Platform, Legacy, Credential (nil: unchanged)
//	add_folder     Name
//	rename_folder  Folder, To
//	remove_folder  Folder (and every session in it)
type Op struct {
	Op         string   `json:"op"`
	Folder     string   `json:"folder,omitempty"`
	Key        string   `json:"key,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Session    *Fields  `json:"session,omitempty"`
	To         string   `json:"to,omitempty"`
	Name       string   `json:"name,omitempty"`
	Platform   *string  `json:"platform,omitempty"`
	Legacy     *bool    `json:"legacy,omitempty"`
	Credential *string  `json:"credential,omitempty"`
}

// ApplyResult is what an Apply changed that a caller holding keys needs.
type ApplyResult struct {
	// Keys maps each original key whose session changed key or was deleted
	// to its new key, "" for deleted. Keys that did not change are absent.
	Keys map[string]string `json:"keys"`
	// Added lists the keys of sessions a save created.
	Added []string `json:"added"`
}

// Apply runs ops against the inventory at path and saves once. Nothing is
// written if any op fails; the error names the op by position.
func Apply(path string, ops []Op) (ApplyResult, error) {
	tree, err := sessions.LoadFile(path)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("inventory %s: %w", path, err)
	}
	ed := &editor{tree: &tree, moved: map[string]string{}, origin: map[string]string{}}
	for i, op := range ops {
		if err := ed.apply(op); err != nil {
			return ApplyResult{}, fmt.Errorf("edit %d (%s): %w", i+1, op.Op, err)
		}
	}
	if err := sessions.SaveFile(path, tree); err != nil {
		return ApplyResult{}, fmt.Errorf("save inventory: %w", err)
	}
	res := ApplyResult{Keys: map[string]string{}, Added: []string{}}
	for orig, now := range ed.moved {
		if orig != now {
			res.Keys[orig] = now
		}
	}
	for _, k := range ed.added {
		if ed.alive(k) {
			res.Added = append(res.Added, k)
		}
	}
	return res, nil
}

type editor struct {
	tree *sessions.Tree
	// moved maps an original key to where it is now ("" deleted); origin
	// maps a current key back to its original, so a key edited twice in
	// one Apply still reports original -> final.
	moved  map[string]string
	origin map[string]string
	added  []string
}

func (ed *editor) rekey(from, to string) {
	orig, ok := ed.origin[from]
	if !ok {
		orig = from
	}
	delete(ed.origin, from)
	ed.moved[orig] = to
	if to != "" {
		ed.origin[to] = orig
	}
}

func (ed *editor) alive(key string) bool {
	_, _, ok := ed.find(key)
	return ok
}

func (ed *editor) find(key string) (int, int, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for i, f := range ed.tree.Folders {
		for j, n := range f.Sessions {
			if n.Key() == key {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func (ed *editor) folderOr(name string) (int, error) {
	i := ed.tree.FolderIndex(name)
	if i < 0 {
		return 0, fmt.Errorf("no folder called %q", name)
	}
	return i, nil
}

func (ed *editor) apply(op Op) error {
	switch op.Op {
	case "save":
		return ed.save(op)
	case "delete":
		for _, k := range op.Keys {
			i, j, ok := ed.find(k)
			if !ok {
				return fmt.Errorf("no session with key %q", k)
			}
			f := &ed.tree.Folders[i]
			f.Sessions = append(f.Sessions[:j], f.Sessions[j+1:]...)
			ed.rekey(strings.ToLower(strings.TrimSpace(k)), "")
		}
		return nil
	case "move":
		if _, err := ed.folderOr(op.To); err != nil {
			return err
		}
		for _, k := range op.Keys {
			i, j, ok := ed.find(k)
			if !ok {
				return fmt.Errorf("no session with key %q", k)
			}
			n := ed.tree.Folders[i].Sessions[j]
			if err := ed.tree.Move(ed.tree.Folders[i].Name, n.Label(), op.To); err != nil {
				return err
			}
		}
		return nil
	case "patch":
		for _, k := range op.Keys {
			i, j, ok := ed.find(k)
			if !ok {
				return fmt.Errorf("no session with key %q", k)
			}
			n := &ed.tree.Folders[i].Sessions[j]
			if op.Platform != nil {
				p := strings.TrimSpace(*op.Platform)
				if err := checkPlatform(p); err != nil {
					return err
				}
				n.Platform = p
			}
			if op.Legacy != nil {
				n.LegacyAlgorithms = *op.Legacy
			}
			if op.Credential != nil {
				n.Credential = strings.TrimSpace(*op.Credential)
			}
		}
		return nil
	case "add_folder":
		return ed.tree.AddFolder(op.Name)
	case "rename_folder":
		return ed.tree.RenameFolder(op.Folder, op.To)
	case "remove_folder":
		i, err := ed.folderOr(op.Folder)
		if err != nil {
			return err
		}
		for _, n := range ed.tree.Folders[i].Sessions {
			if k := n.Key(); k != "" {
				ed.rekey(k, "")
			}
		}
		return ed.tree.RemoveFolder(op.Folder, true)
	}
	return fmt.Errorf("unknown edit %q", op.Op)
}

func (ed *editor) save(op Op) error {
	if op.Session == nil {
		return fmt.Errorf("no session fields")
	}
	s := *op.Session
	s.Name, s.Host = strings.TrimSpace(s.Name), strings.TrimSpace(s.Host)
	s.Platform, s.Credential = strings.TrimSpace(s.Platform), strings.TrimSpace(s.Credential)
	if s.Host == "" {
		return fmt.Errorf("a device needs a host")
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("port %d is out of range", s.Port)
	}
	if err := checkPlatform(s.Platform); err != nil {
		return err
	}

	if strings.TrimSpace(op.Key) == "" {
		n := sessions.Node{Name: s.Name, Transport: sessions.TransportSSH, Host: s.Host, Port: s.Port,
			Platform: s.Platform, LegacyAlgorithms: s.Legacy, Credential: s.Credential}.Normalize()
		if _, _, taken := ed.find(n.Key()); taken {
			return fmt.Errorf("another device is already at %s", n.Key())
		}
		if err := ed.tree.Add(op.Folder, n); err != nil {
			return err
		}
		ed.added = append(ed.added, n.Key())
		return nil
	}

	i, j, ok := ed.find(op.Key)
	if !ok {
		return fmt.Errorf("no session with key %q", op.Key)
	}
	old := ed.tree.Folders[i].Sessions[j]
	n := old
	n.Name, n.Host, n.Port = s.Name, s.Host, s.Port
	n.Platform, n.LegacyAlgorithms, n.Credential = s.Platform, s.Legacy, s.Credential
	n = n.Normalize()
	if n.Key() != old.Key() {
		if _, _, taken := ed.find(n.Key()); taken {
			return fmt.Errorf("another device is already at %s", n.Key())
		}
	}
	if err := ed.tree.Replace(ed.tree.Folders[i].Name, old.Label(), n); err != nil {
		return err
	}
	if n.Key() != old.Key() {
		ed.rekey(old.Key(), n.Key())
	}
	return nil
}

func checkPlatform(p string) error {
	if p != "" && !netexec.KnownPlatform(p) {
		return fmt.Errorf("platform %q is not one capture knows", p)
	}
	return nil
}

// Platforms is every platform a device can be set to.
func Platforms() []string { return netexec.Platforms() }
