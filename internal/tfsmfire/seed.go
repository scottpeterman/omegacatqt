// internal/tfsmfire/seed.go
//
// The template database shipped with the application, and its first-run copy.
package tfsmfire

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sirikothe/gotextfsm"

	"github.com/scottpeterman/omegacatqt/internal/secfile"
)

// DBName is the database's file name in the configuration directory.
const DBName = "tfsm_templates.db"

// seedDB is the database as shipped. It is the scrubbed copy in this
// repository and nothing else: the template tool's "save sample output"
// writes captured output into the USER's copy, which never comes back here,
// and scripts/scrub-check.sh scans this file's contents (not just its name)
// before anything is pushed.
//
//go:embed seed/tfsm_templates.db
var seedDB []byte

// Seed returns the shipped database's bytes.
func Seed() []byte { return seedDB }

// EnsureDB makes sure dir holds a template database, copying the shipped one
// there if not, and returns its path and whether it was just created.
//
// An existing file is never replaced: it is the user's, and it may hold
// templates they wrote or fixed. Bringing new shipped templates into an
// existing database is a merge, keyed on textfsm_hash, and is not this.
func EnsureDB(dir string) (path string, created bool, err error) {
	path = filepath.Join(dir, DBName)
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("tfsmfire: %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("tfsmfire: create %s: %w", dir, err)
	}
	if err := secfile.WriteAtomic(path, seedDB); err != nil {
		return "", false, fmt.Errorf("tfsmfire: write %s: %w", path, err)
	}
	return path, true, nil
}

// compileCheck reports whether src compiles.
func compileCheck(src string) error {
	var fsm gotextfsm.TextFSM
	return fsm.ParseString(src)
}

// round1 rounds to one decimal place the way Python's round(x, 1) does: on the
// exact binary value, halves to even. strconv's shortest-correct formatting
// gives that; math.Round(x*10)/10 does not always.
func round1(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 1, 64), 64)
	return r
}
