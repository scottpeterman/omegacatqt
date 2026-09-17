// internal/capture/parsed.go
//
// Structured parses of stored captures, written beside the raw file.
//
//	<type>/2026-09-15T02-15-00Z.txt           what the device said
//	<type>/2026-09-15T02-15-00Z.parsed.json   what TextFSM made of it
//
// The raw file stays the record. A parse is derived from it and from whatever
// the template database held at the time, so it is written on every capture of
// a parsed type, unchanged ones included: a template fixed yesterday improves
// last week's unchanged MAC table tonight, without anyone re-collecting it.
//
// A parse never fails a capture. The output is on disk before parsing starts;
// a template that matches nothing, or a database that cannot be read, costs the
// structured view of that one file and nothing else.
package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/tfsmfire"
)

// MinParseScore is the lowest tfsm-fire score accepted as a parse, netlapse's
// threshold: it filters junk matches and keeps legitimate single-record ones.
const MinParseScore = 15.0

// Parse outcomes.
const (
	ParseStatusParsed  = "parsed"   // a template scored at least MinParseScore
	ParseStatusNoMatch = "no-match" // none did; Template and Score say how close
)

// Parser picks and runs a template. *tfsmfire.Engine is one.
type Parser interface {
	FindBest(output, hint string) tfsmfire.Match
}

// ParsedFile is a .parsed.json sidecar.
type ParsedFile struct {
	Status   string            `json:"status"`
	Template string            `json:"template,omitempty"`
	Score    float64           `json:"score"`
	Hint     string            `json:"hint"`
	Tried    int               `json:"tried"`
	Header   []string          `json:"header"`
	Records  []tfsmfire.Record `json:"records"`
	ParsedAt time.Time         `json:"parsed_at"`

	// RawFile and RawSHA256 tie the parse to the exact bytes it was made
	// from. A reader that finds a mismatch has a sidecar left over from
	// something else and should not show it.
	RawFile   string `json:"raw_file"`
	RawSHA256 string `json:"raw_sha256"`
}

// ParsedName is the sidecar name for a stored capture file.
func ParsedName(file string) string {
	return strings.TrimSuffix(file, ".txt") + ".parsed.json"
}

// ParsedStore is a Store that can hold parses. Separate from Store so an
// implementation without it -- a test fake, a future remote store -- still
// captures, and simply does not parse.
type ParsedStore interface {
	PutParsed(canonical, typ string, parsed ParsedFile) error
}

// BuildParsed runs p over content and describes the result. Records are
// dropped from a no-match: a best-of-nothing parse is noise in a table, while
// the template name and score stay, because "closest was X at 9.2" is exactly
// what someone fixing the template needs.
func BuildParsed(p Parser, hint string, content []byte, rawFile, rawSHA string, at time.Time) ParsedFile {
	m := p.FindBest(string(content), hint)
	pf := ParsedFile{
		Status: ParseStatusNoMatch, Template: m.Template, Score: m.Score,
		Hint: hint, Tried: m.Tried, Header: []string{}, Records: []tfsmfire.Record{},
		ParsedAt: at.UTC(), RawFile: rawFile, RawSHA256: rawSHA,
	}
	if m.Template != "" && m.Score >= MinParseScore && len(m.Records) > 0 {
		pf.Status = ParseStatusParsed
		pf.Header = m.Header
		pf.Records = m.Records
	}
	return pf
}

// PutParsed writes the sidecar for parsed.RawFile, replacing any earlier one.
func (s *FileStore) PutParsed(canonical, typ string, parsed ParsedFile) error {
	slug, err := Slug(canonical)
	if err != nil {
		return err
	}
	t, err := element("capture type", typ)
	if err != nil {
		return err
	}
	f, err := element("capture file", parsed.RawFile)
	if err != nil {
		return err
	}
	data, err := json.Marshal(parsed)
	if err != nil {
		return fmt.Errorf("capture: encode parse of %s: %w", f, err)
	}

	lk := s.lockFor(slug)
	lk.Lock()
	defer lk.Unlock()
	typeDir := filepath.Join(s.root, "devices", slug, t)
	// The raw file has to exist: a sidecar for a capture that was pruned in
	// the meantime is exactly the orphan the sweep exists to remove.
	if !fileExists(filepath.Join(typeDir, f)) {
		return fmt.Errorf("capture: no stored file %s for %s/%s", f, canonical, typ)
	}
	return writeFileAtomic(filepath.Join(typeDir, ParsedName(f)), data)
}

// ReadParsed returns the sidecar for one stored file, and false when there is
// none -- a type that is not parsed, or a capture from before parsing.
func (s *FileStore) ReadParsed(canonical, typ, file string) (ParsedFile, bool, error) {
	dir, err := s.deviceDir(canonical)
	if err != nil {
		return ParsedFile{}, false, err
	}
	t, err := element("capture type", typ)
	if err != nil {
		return ParsedFile{}, false, err
	}
	f, err := element("capture file", file)
	if err != nil {
		return ParsedFile{}, false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, t, ParsedName(f)))
	if os.IsNotExist(err) {
		return ParsedFile{}, false, nil
	}
	if err != nil {
		return ParsedFile{}, false, err
	}
	var pf ParsedFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return ParsedFile{}, false, fmt.Errorf("capture: %s: %w", ParsedName(f), err)
	}
	return pf, true, nil
}
