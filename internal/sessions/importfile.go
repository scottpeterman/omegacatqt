// internal/sessions/importfile.go
//
// Deciding what a file the person picked actually is, and merging it in.
//
// The tree already knows how to read three shapes — its own mapping, the older
// terminal's bare list of folders, and a crawl's map.json. What it did not have
// is the step in front of those: somebody chooses a file from a menu, and the
// application has to work out which reader to hand it to, then say what the
// import did in terms worth reading.
//
// Both halves live here rather than in the window, for the same reason the rest
// of this package does: sniffing a format and counting what an import changed
// are rules, and rules that need a display to test stop being tested. The dialog
// contributes a file path and a folder name; everything else is here.
//
// # Why sniff rather than trust the extension
//
// A session file and a map are both routinely called .yaml and .json by people
// who have several of each, and the older terminal's file has no extension of
// its own at all. Guessing from the name would produce the one failure that is
// hard to explain: a file that parses far enough to import the wrong thing. So
// the shape of the document decides — a top-level sequence is the older
// terminal's, a mapping carrying folders is this one's, and any other mapping is
// a map. The menu item the person chose is then a claim that can be checked
// against the file, which is how "that looks like a topology map" becomes a
// message instead of an empty import.
package sessions

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format is what a picked file turned out to be.
type Format int

const (
	// FormatUnknown is a file this application has no reader for. It is also
	// what an unparseable file reports: the difference does not change what
	// happens next, and claiming to know which one it was would be a guess.
	FormatUnknown Format = iota
	// FormatNative is this application's own session file.
	FormatNative
	// FormatTether is the older terminal's session file: a bare list of
	// folders, SSH only, with a string port.
	FormatTether
	// FormatMap is a crawl's map.json.
	FormatMap
)

// String names the format the way a message to the person would.
func (f Format) String() string {
	switch f {
	case FormatNative:
		return "session file"
	case FormatTether:
		return "TetherSSH session file"
	case FormatMap:
		return "topology map"
	default:
		return "unrecognised file"
	}
}

// Sniff decides what a file is from its shape.
//
// An empty file is FormatNative rather than unknown: an empty session file is
// a legitimate thing to hold, and reporting it as garbage would be wrong about
// the one case that is always safe to import.
func Sniff(data []byte) Format {
	if len(bytes.TrimSpace(data)) == 0 {
		return FormatNative
	}

	// A yaml.Node decode answers the only question that matters here — what
	// kind of thing is at the top — without needing a struct that matches
	// any of the three. JSON is valid YAML, so map.json arrives here too.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return FormatUnknown
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return FormatNative
		}
		root = root.Content[0]
	}

	switch root.Kind {
	case yaml.SequenceNode:
		return FormatTether
	case yaml.MappingNode:
		if len(root.Content) == 0 {
			return FormatNative
		}
		// Mapping keys sit at even indices. Either of this file's own
		// top-level keys settles it; a map has neither, because its keys
		// are device names.
		for i := 0; i+1 < len(root.Content); i += 2 {
			switch strings.ToLower(root.Content[i].Value) {
			case "folders", "version":
				return FormatNative
			}
		}
		return FormatMap
	default:
		return FormatUnknown
	}
}

// FoldersFrom reads a session file of either shape and returns its folders.
//
// It refuses a map rather than trying: a map has no folders in it, and the
// import that produced nothing is worse than the one that explained itself.
// The caller gets the format back so the message can name what the file was.
func FoldersFrom(data []byte) ([]Folder, Format, error) {
	format := Sniff(data)
	switch format {
	case FormatNative:
		t, err := UnmarshalTree(data)
		if err != nil {
			return nil, format, err
		}
		return t.Folders, format, nil
	case FormatTether:
		f, err := ImportTether(data)
		if err != nil {
			return nil, format, err
		}
		return f, format, nil
	case FormatMap:
		return nil, format, fmt.Errorf("that is a topology map, not a session file — import it as a map")
	default:
		return nil, format, fmt.Errorf("not a session file this application can read")
	}
}

// ImportSummary is what a whole import did, across however many folders it
// touched. It is the counted form of one or more ImportResults, because the
// question after an import is "how much of that did I already have", and the
// answer has to survive a file that contributed to six folders.
type ImportSummary struct {
	// Folders are the destination folders that gained something, in the
	// order they were filled.
	Folders []string
	// Created are the folders this import brought into existence.
	Created []string

	Added   int
	Skipped int

	Renamed  []string // added under a different name to avoid a clash
	Rejected []string // nothing to connect to, or the folder refused it
}

// merge folds one folder's result into the summary.
func (s *ImportSummary) merge(r ImportResult) {
	if r.Added > 0 {
		s.Folders = append(s.Folders, r.Folder)
	}
	s.Added += r.Added
	s.Skipped += len(r.Skipped)
	s.Renamed = append(s.Renamed, r.Renamed...)
	s.Rejected = append(s.Rejected, r.Rejected...)
}

// Describe is the text to put in front of the person afterwards.
//
// It leads with what was added, because that is the thing that changed, and it
// says nothing at all about the categories that were empty — a summary with four
// zeroes in it reads as a failure even when it is a clean re-import.
func (s ImportSummary) Describe() string {
	var b strings.Builder

	switch {
	case s.Added == 0 && s.Skipped > 0:
		fmt.Fprintf(&b, "Nothing new — all %d were already in the tree.", s.Skipped)
	case s.Added == 0:
		b.WriteString("Nothing was imported.")
	case len(s.Folders) == 1:
		fmt.Fprintf(&b, "Added %s to %q.", plural(s.Added, "session"), s.Folders[0])
	default:
		fmt.Fprintf(&b, "Added %s across %s.", plural(s.Added, "session"), plural(len(s.Folders), "folder"))
	}

	if s.Added > 0 && s.Skipped > 0 {
		fmt.Fprintf(&b, "\nAlready in the tree: %d.", s.Skipped)
	}
	if len(s.Created) > 0 {
		fmt.Fprintf(&b, "\nNew folder(s): %s.", nameList(s.Created))
	}
	if len(s.Renamed) > 0 {
		fmt.Fprintf(&b, "\nRenamed to avoid a clash: %s.", nameList(s.Renamed))
	}
	if len(s.Rejected) > 0 {
		fmt.Fprintf(&b, "\nNo address to connect to, so not imported: %s.", nameList(s.Rejected))
	}
	return b.String()
}

// ImportFolders merges a whole session file into the tree.
//
// Each source folder keeps its own name, because that structure is the part a
// person authored by hand and the part an import has no business flattening.
// Sessions are still skipped tree-wide by address, so re-importing the same
// file — or a colleague's file that overlaps with this one — adds only what is
// genuinely new and leaves every name and setting already edited alone.
//
// An empty folder in the source is created anyway. It is structure somebody
// made deliberately, and an import that silently dropped it would be lossy in a
// way nothing reports.
func (t *Tree) ImportFolders(folders []Folder) ImportSummary {
	var s ImportSummary

	for _, f := range folders {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = "Imported"
		}
		isNew := t.FolderIndex(name) < 0

		if len(f.Sessions) == 0 {
			if isNew {
				if err := t.AddFolder(name); err == nil {
					s.Created = append(s.Created, name)
				}
			}
			continue
		}

		res := t.Import(name, f.Sessions)
		// Import creates the folder lazily, through Add. So it exists now
		// only if something actually landed in it — which is exactly when
		// it is worth telling the person a folder appeared.
		if isNew && t.FolderIndex(name) >= 0 {
			s.Created = append(s.Created, name)
		}
		s.merge(res)
	}
	return s
}

// nameList renders a handful of names and counts the rest. A dialog listing
// four hundred skipped devices is a dialog nobody reads to the end of.
func nameList(names []string) string {
	const max = 6
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:max], ", "), len(names)-max)
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
