// internal/inventory/inventory.go
//
// OmegaCat's own inventory: a session file under ~/.omegacat, filled by
// importing omegamaps' map.json.
//
// # Why a file of its own
//
// A device list typed into the Run view is gone the moment the box is cleared,
// and pointing OmegaCat at another tool's session file would make that tool a
// dependency: its format changes, its path moves, and OmegaCat breaks for a
// reason that is not in this repository. The inventory is OmegaCat's, in the
// session-file format capture already selects from (capturerun.Params
// SessionFile + Match), so nothing downstream of this package knows where the
// devices came from.
//
// # What a map import is
//
// map.json is an input, never a live reference: its devices are copied in and
// the file is not read again. Each import goes into one folder. Re-importing the
// same map, or a later crawl of the same estate, adds only devices whose address
// is new -- sessions.Tree.Import skips by transport:host:port tree-wide -- so a
// name somebody fixed by hand is never overwritten by the next crawl.
//
// A device already in the inventory is recognised by its address anywhere, or
// by its name in the folder being imported into. The name matters once devices
// are edited: change eng-leaf-1's address by hand, re-import the crawl that
// still has the old one, and matching on address alone would add a second
// eng-leaf-1. A recognised device is left alone except for DeviceType, the
// crawl's platform guess, which is refreshed when the crawl's guess changed.
// Platform, legacy and credential are a person's and an import never writes
// them.
//
// Leaves (peers a neighbour named but the crawl never dialled) are left out:
// they are mostly servers and phones behind a filter, and a capture inventory
// of things that do not answer SSH is noise.
package inventory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/scottpeterman/omegacatqt/internal/sessions"
	"github.com/scottpeterman/omegacatqt/internal/vaultcli"
)

// FileName is the inventory's name inside the application directory.
const FileName = "inventory.yaml"

// DefaultPath is ~/.omegacat/inventory.yaml, or the bare file name when there
// is no home directory.
func DefaultPath() string {
	dir := vaultcli.AppDir()
	if dir == "" {
		return FileName
	}
	return filepath.Join(dir, FileName)
}

// Session is one inventory entry as a view shows it.
type Session struct {
	// Key is transport:host:port, what a capture request's session_keys
	// selects on.
	Key       string `json:"key"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port,omitempty"`
	Transport string `json:"transport"`
	// Platform is the authority a person set; DeviceType the import's
	// guess. Either may be empty.
	Platform   string `json:"platform,omitempty"`
	DeviceType string `json:"device_type,omitempty"`
	Legacy     bool   `json:"legacy,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Folder is one folder and its sessions, in file order.
type Folder struct {
	Name     string    `json:"name"`
	Sessions []Session `json:"sessions"`
}

// View is the whole inventory for display.
type View struct {
	Path    string   `json:"path"`
	Folders []Folder `json:"folders"`
}

// Load reads the inventory. A file that does not exist is an empty inventory.
func Load(path string) (View, error) {
	t, err := sessions.LoadFile(path)
	if err != nil {
		return View{}, fmt.Errorf("inventory %s: %w", path, err)
	}
	v := View{Path: path, Folders: make([]Folder, 0, len(t.Folders))}
	for _, f := range t.Folders {
		out := Folder{Name: f.Name, Sessions: make([]Session, 0, len(f.Sessions))}
		for _, n := range f.Sessions {
			out.Sessions = append(out.Sessions, Session{
				Key:        n.Key(),
				Name:       n.Label(),
				Host:       strings.TrimSpace(n.Host),
				Port:       n.Port,
				Transport:  string(n.Transport),
				Platform:   strings.TrimSpace(n.Platform),
				DeviceType: strings.TrimSpace(n.DeviceType),
				Legacy:     n.LegacyAlgorithms,
				Credential: strings.TrimSpace(n.Credential),
			})
		}
		v.Folders = append(v.Folders, out)
	}
	return v, nil
}

// ImportResult is what one map import did.
type ImportResult struct {
	Folder  string `json:"folder"`
	Created bool   `json:"created"`
	Added   int    `json:"added"`
	Skipped int    `json:"skipped"`
	// Refreshed counts devices already present whose DeviceType the map
	// changed. They are also counted in Skipped.
	Refreshed int      `json:"refreshed"`
	Renamed   []string `json:"renamed"`
	Rejected  []string `json:"rejected"`
	Message   string   `json:"message"`
}

// FolderForMap is the folder name an import uses when none is given: the file
// name without its extension, or the directory holding it when the file is
// just called map (omegamaps writes map.json into a per-crawl directory, so the
// directory is the name that tells two crawls apart).
func FolderForMap(mapPath string) string {
	base := filepath.Base(mapPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.EqualFold(stem, "map") || stem == "" {
		if dir := filepath.Base(filepath.Dir(mapPath)); dir != "." && dir != string(filepath.Separator) && dir != "" {
			return dir
		}
	}
	if stem == "" {
		return "Imported"
	}
	return stem
}

// ImportMap copies the devices in mapPath into folder (FolderForMap when
// empty) and saves the inventory.
//
// The file has to be a map. A session file or anything else is refused by
// shape, not by extension: both are routinely called .json.
func ImportMap(invPath, mapPath, folder string) (ImportResult, error) {
	data, err := os.ReadFile(mapPath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("read map: %w", err)
	}
	if f := sessions.Sniff(data); f != sessions.FormatMap {
		return ImportResult{}, fmt.Errorf("%s is a %s, not an omegamaps map.json", filepath.Base(mapPath), f)
	}
	nodes, err := sessions.NodesFromMap(data, false)
	if err != nil {
		return ImportResult{}, err
	}

	folder = strings.TrimSpace(folder)
	if folder == "" {
		folder = FolderForMap(mapPath)
	}
	tree, err := sessions.LoadFile(invPath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("inventory %s: %w", invPath, err)
	}
	fresh, known, refreshed := recognise(&tree, folder, nodes)
	sum := tree.ImportFolders([]sessions.Folder{{Name: folder, Sessions: fresh}})

	res := ImportResult{
		Folder:    folder,
		Created:   len(sum.Created) > 0,
		Added:     sum.Added,
		Skipped:   sum.Skipped + known,
		Refreshed: refreshed,
		Renamed:   nonNil(sum.Renamed),
		Rejected:  nonNil(sum.Rejected),
	}
	res.Message = describe(res)
	// Nothing new or changed is still a successful import, and not worth
	// rewriting the file for.
	if sum.Added == 0 && len(sum.Created) == 0 && refreshed == 0 {
		return res, nil
	}
	if err := sessions.SaveFile(invPath, tree); err != nil {
		return ImportResult{}, fmt.Errorf("save inventory: %w", err)
	}
	return res, nil
}

// recognise splits an import into devices the inventory does not have yet and
// devices it does, refreshing DeviceType on the ones it does. A device is known
// by its key anywhere in the tree, or by its name in the destination folder.
func recognise(tree *sessions.Tree, folder string, nodes []sessions.Node) (fresh []sessions.Node, known, refreshed int) {
	type loc struct{ f, s int }
	byKey := map[string]loc{}
	byName := map[string]loc{}
	for i, f := range tree.Folders {
		for j, n := range f.Sessions {
			if k := n.Key(); k != "" {
				byKey[k] = loc{i, j}
			}
			if strings.EqualFold(f.Name, folder) {
				byName[strings.ToLower(n.Label())] = loc{i, j}
			}
		}
	}
	for _, n := range nodes {
		n = n.Normalize()
		at, ok := byKey[n.Key()]
		if !ok {
			at, ok = byName[strings.ToLower(n.Label())]
		}
		if !ok {
			fresh = append(fresh, n)
			continue
		}
		known++
		existing := &tree.Folders[at.f].Sessions[at.s]
		if hint := strings.TrimSpace(n.DeviceType); hint != "" && hint != existing.DeviceType {
			existing.DeviceType = hint
			refreshed++
		}
	}
	return fresh, known, refreshed
}

func describe(r ImportResult) string {
	var parts []string
	switch {
	case r.Added == 1:
		parts = append(parts, fmt.Sprintf("Added 1 device to %s", r.Folder))
	default:
		parts = append(parts, fmt.Sprintf("Added %d devices to %s", r.Added, r.Folder))
	}
	if r.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d already in the inventory", r.Skipped))
	}
	if r.Refreshed > 0 {
		parts = append(parts, fmt.Sprintf("%d with an updated platform guess", r.Refreshed))
	}
	msg := strings.Join(parts, "; ") + "."
	if len(r.Renamed) > 0 {
		msg += "\nRenamed to avoid a clash: " + strings.Join(r.Renamed, ", ")
	}
	if len(r.Rejected) > 0 {
		msg += "\nNot added (no address): " + strings.Join(r.Rejected, ", ")
	}
	return msg
}

// RemoveFolder deletes a folder and every session in it.
func RemoveFolder(invPath, folder string) error {
	tree, err := sessions.LoadFile(invPath)
	if err != nil {
		return fmt.Errorf("inventory %s: %w", invPath, err)
	}
	if err := tree.RemoveFolder(folder, true); err != nil {
		return err
	}
	return sessions.SaveFile(invPath, tree)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
