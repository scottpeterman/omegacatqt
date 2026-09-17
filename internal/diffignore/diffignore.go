// internal/diffignore/diffignore.go
//
// Lines a config diff shows dimmed and does not count.
//
// Some lines in a stored config change only because something else did:
// IOS rewrites "Current configuration : N bytes" and "! Last configuration
// change at ..." on every edit. A diff that counts them reports three changes
// for a one-line edit and opens a hunk at the top of the file for it. These
// rules name such lines per platform so a diff can show them dimmed and leave
// them out of its counts and hunks.
//
// DISPLAY ONLY. Nothing here decides whether a capture is stored or reported
// unchanged; that still compares every byte. A line that changes with no
// config change at all (IOS "ntp clock-period") would need that too, and it is
// a different decision with a different cost.
//
// # The file
//
// ~/.omegacat/diff-ignore.yaml, written with Defaults the first time it is
// needed and read on every diff, so an edit applies to the next one:
//
//	platforms:
//	  cisco_ios:
//	    - '^Current configuration : \d+ bytes$'
//	  "*":
//	    - '...'   # every platform
//
// Patterns are Go regular expressions matched against one line without its
// line ending. A pattern that does not compile is reported with the file and
// platform and the rest still apply.
package diffignore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/scottpeterman/omegacatqt/internal/vaultcli"
)

// FileName is the rules file inside the application directory.
const FileName = "diff-ignore.yaml"

// Defaults is what a new rules file contains.
//
// The IOS and IOS-XE lines are the header IOS writes into every running-config
// and startup-config. The NX-OS and Junos lines are that platform's equivalent
// header as documented; check them against your own gear's output.
const Defaults = `# Lines a config diff shows dimmed and leaves out of its counts.
# Display only: a capture is still stored or unchanged on every byte.
# Go regular expressions, matched against one line. "*" applies to every platform.
platforms:
  cisco_ios: &ios_header
    - '^Building configuration\.\.\.$'
    - '^Current configuration : \d+ bytes$'
    - '^! Last configuration change at '
    - '^! NVRAM config last updated at '
    - '^! No configuration change since last restart'
  cisco_iosxe: *ios_header
  cisco_nxos:
    - '^!Time: '
    - '^!Running configuration last done at: '
  juniper_junos:
    - '^## Last commit: '
`

// Path is ~/.omegacat/diff-ignore.yaml.
func Path() string {
	if dir := vaultcli.AppDir(); dir != "" {
		return filepath.Join(dir, FileName)
	}
	return FileName
}

type file struct {
	Platforms map[string][]string `yaml:"platforms"`
}

// Rules are compiled patterns by platform.
type Rules struct {
	byPlatform map[string][]*regexp.Regexp
}

// For returns the patterns for a platform, including "*".
func (r Rules) For(platform string) []*regexp.Regexp {
	out := append([]*regexp.Regexp(nil), r.byPlatform["*"]...)
	return append(out, r.byPlatform[strings.TrimSpace(platform)]...)
}

// Parse compiles a rules document. Patterns that do not compile are skipped
// and returned together as one error; the rest are usable.
func Parse(data []byte) (Rules, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return Rules{}, fmt.Errorf("parse: %w", err)
	}
	r := Rules{byPlatform: map[string][]*regexp.Regexp{}}
	var bad []string
	for platform, pats := range f.Platforms {
		for _, p := range pats {
			re, err := regexp.Compile(p)
			if err != nil {
				bad = append(bad, fmt.Sprintf("%s: %q: %v", platform, p, err))
				continue
			}
			r.byPlatform[platform] = append(r.byPlatform[platform], re)
		}
	}
	if len(bad) > 0 {
		return r, fmt.Errorf("patterns skipped: %s", strings.Join(bad, "; "))
	}
	return r, nil
}

// Load reads the rules at path, writing Defaults there first when there is no
// file. A file that cannot be written is not an error: Defaults still apply.
func Load(path string) (Rules, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data = []byte(Defaults)
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
			_ = os.WriteFile(path, data, 0o600)
		}
	} else if err != nil {
		return Rules{}, fmt.Errorf("%s: %w", path, err)
	}
	r, err := Parse(data)
	if err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
