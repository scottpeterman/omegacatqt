package storesearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/scottpeterman/omegacatqt/internal/capture"
)

// fakeStore is a capture.Browser over a map.
//
// It counts History calls rather than merely not implementing them, because
// "the search never reads history" is a design decision with a cost attached —
// history is where the same bytes appear once per nightly attempt — and a
// decision that nothing checks is a decision that quietly stops being true.
type fakeStore struct {
	devices  []capture.DeviceInfo
	warn     error
	types    map[string][]capture.TypeInfo
	typesErr map[string]error
	files    map[string][]byte

	historyCalls atomic.Int64
}

func key(device, typ, file string) string { return device + "|" + typ + "|" + file }

func (f *fakeStore) Devices() ([]capture.DeviceInfo, error) { return f.devices, f.warn }

func (f *fakeStore) Types(canonical string) ([]capture.TypeInfo, error) {
	if err, ok := f.typesErr[canonical]; ok {
		return nil, err
	}
	return f.types[canonical], nil
}

func (f *fakeStore) History(canonical, typ string) ([]capture.HistoryEntry, error) {
	f.historyCalls.Add(1)
	return nil, nil
}

func (f *fakeStore) Read(canonical, typ, file string) ([]byte, error) {
	b, ok := f.files[key(canonical, typ, file)]
	if !ok {
		return nil, fmt.Errorf("no such artifact")
	}
	return b, nil
}

// labStore builds a small store with lab naming throughout.
func labStore() *fakeStore {
	f := &fakeStore{
		types: map[string][]capture.TypeInfo{},
		files: map[string][]byte{},
	}
	add := func(device, typ, file, body string) {
		f.types[device] = append(f.types[device], capture.TypeInfo{Type: typ, File: file, Stored: 1})
		f.files[key(device, typ, file)] = []byte(body)
	}
	for _, d := range []string{"eng-rtr-1", "usa-leaf-1", "eng-spine-1"} {
		f.devices = append(f.devices, capture.DeviceInfo{Canonical: d})
	}
	add("eng-rtr-1", "running-config", "a.txt",
		"hostname eng-rtr-1\n!\nrouter bgp 65001\n bgp router-id 10.0.0.1\n neighbor 10.0.0.2 remote-as 65002\n")
	add("eng-rtr-1", "inventory", "b.txt", "NAME: chassis\nPID: lab-7200\n")
	add("usa-leaf-1", "running-config", "c.txt",
		"hostname usa-leaf-1\n!\nrouter bgp 65001\n")
	add("eng-spine-1", "running-config", "d.txt", "hostname eng-spine-1\nno bgp default\n")
	return f
}

func mustLiteral(t *testing.T, q string) Matcher {
	t.Helper()
	m, err := NewLiteral(q, false)
	if err != nil {
		t.Fatalf("NewLiteral(%q): %v", q, err)
	}
	return m
}

// The order the disk answers in is not the order the table renders in. Without
// the per-target collection this test reshuffles between runs, which is what a
// user reads as the list being unstable.
func TestHitsComeBackSortedByDeviceThenTypeThenLine(t *testing.T) {
	res, err := Search(context.Background(), labStore(), mustLiteral(t, "bgp"), Options{Concurrency: 4})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var got []string
	for _, h := range res.Hits {
		got = append(got, fmt.Sprintf("%s/%s:%d", h.Device, h.Type, h.Line))
	}
	want := []string{
		"eng-rtr-1/running-config:3",
		"eng-rtr-1/running-config:4",
		"eng-spine-1/running-config:2",
		"usa-leaf-1/running-config:3",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}
}

// The whole reason current-only was chosen: history holds one entry per
// attempt, and unchanged attempts name a file that already exists.
func TestHistoryIsNeverRead(t *testing.T) {
	f := labStore()
	if _, err := Search(context.Background(), f, mustLiteral(t, "hostname"), Options{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if n := f.historyCalls.Load(); n != 0 {
		t.Fatalf("History called %d times; the search must read only the current version", n)
	}
}

func TestTheCapStopsTheScanAndSaysSo(t *testing.T) {
	f := labStore()
	res, err := Search(context.Background(), f, mustLiteral(t, "o"), Options{Limit: 2, Concurrency: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !res.Capped {
		t.Fatal("Capped is false; a truncated list that does not admit it is a wrong answer")
	}
	if len(res.Hits) != 2 {
		t.Fatalf("held %d hits, want the limit of 2", len(res.Hits))
	}
	if !strings.Contains(res.Summary(), "stopped at 2") {
		t.Fatalf("summary hides the cap: %q", res.Summary())
	}
}

func TestUnsearchableArtifactsAreReportedNotSilentlyDropped(t *testing.T) {
	f := labStore()
	f.types["eng-spine-1"] = append(f.types["eng-spine-1"],
		capture.TypeInfo{Type: "core-dump", File: "e.bin", Stored: 1})
	f.files[key("eng-spine-1", "core-dump", "e.bin")] = []byte{0xff, 0xfe, 0x00, 0x01}
	f.types["usa-leaf-1"] = append(f.types["usa-leaf-1"],
		capture.TypeInfo{Type: "huge", File: "f.txt", Stored: 1})
	f.files[key("usa-leaf-1", "huge", "f.txt")] = []byte(strings.Repeat("x", 4096))
	f.types["eng-rtr-1"] = append(f.types["eng-rtr-1"],
		capture.TypeInfo{Type: "gone", File: "missing.txt", Stored: 1})

	res, err := Search(context.Background(), f, mustLiteral(t, "bgp"), Options{MaxFileBytes: 1024})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := map[SkipReason]bool{}
	for _, s := range res.Skips {
		got[s.Reason] = true
	}
	for _, want := range []SkipReason{SkipNotText, SkipTooLarge, SkipUnread} {
		if !got[want] {
			t.Errorf("no %q skip recorded; skips = %v", want, res.Skips)
		}
	}
	// A device that could not be searched must not silently reduce the
	// result to "no matches here".
	if !strings.Contains(res.Summary(), "skipped") {
		t.Errorf("summary hides skips: %q", res.Summary())
	}
}

// capture.Devices returns a usable list ALONGSIDE UnreadableDevices. A search
// that treats that as a failure shows nothing because one directory is damaged.
func TestADamagedDeviceDirectoryDoesNotFailTheSearch(t *testing.T) {
	f := labStore()
	f.warn = capture.UnreadableDevices{"half-written"}

	res, err := Search(context.Background(), f, mustLiteral(t, "bgp"), Options{})
	if err != nil {
		t.Fatalf("Search returned an error for a partial device list: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("no hits; the readable devices were dropped with the damaged one")
	}
	if res.Warning == nil {
		t.Fatal("Warning is nil; the damaged directory is now invisible")
	}
}

// Devices returning nothing at all IS a failure — there is no partial list to
// show, and reporting "no matches" for a store that could not be opened is the
// same wrong answer in a friendlier voice.
func TestAStoreThatCannotBeListedIsAnError(t *testing.T) {
	f := &fakeStore{warn: errors.New("open store: permission denied")}
	if _, err := Search(context.Background(), f, mustLiteral(t, "bgp"), Options{}); err == nil {
		t.Fatal("no error for a store that could not be listed")
	}
}

func TestTypeFilterRestrictsWhatIsRead(t *testing.T) {
	res, err := Search(context.Background(), labStore(), mustLiteral(t, "lab-7200"),
		Options{Types: []string{"running-config"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("matched inside a type that was filtered out: %+v", res.Hits)
	}
	if res.Artifacts != 3 {
		t.Fatalf("read %d artifacts, want the 3 running-configs", res.Artifacts)
	}
}

func TestIndentIsRecordedWhenTheLineIsTrimmed(t *testing.T) {
	res, err := Search(context.Background(), labStore(), mustLiteral(t, "neighbor"), Options{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(res.Hits))
	}
	h := res.Hits[0]
	if strings.HasPrefix(h.Text, " ") {
		t.Errorf("Text %q was not trimmed", h.Text)
	}
	if h.Indent != 1 {
		t.Errorf("Indent = %d, want 1 — trimming must not destroy the fact that the line was inside a block", h.Indent)
	}
}

func TestALongLineIsTruncatedOnARuneBoundary(t *testing.T) {
	f := labStore()
	f.files[key("eng-rtr-1", "running-config", "a.txt")] =
		[]byte("description " + strings.Repeat("é", 100) + "\n")

	res, err := Search(context.Background(), f, mustLiteral(t, "description"), Options{TrimTo: 21})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(res.Hits))
	}
	h := res.Hits[0]
	if !h.Truncated {
		t.Error("Truncated is false on a truncated line")
	}
	if !strings.HasSuffix(h.Text, "…") {
		t.Errorf("no ellipsis: %q", h.Text)
	}
	if strings.ContainsRune(h.Text, '\uFFFD') {
		t.Errorf("cut mid-rune: %q", h.Text)
	}
}

func TestACancelledSearchStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Search(ctx, labStore(), mustLiteral(t, "bgp"), Options{})
	if err == nil {
		t.Fatal("no error from a cancelled search")
	}
	if len(res.Hits) > 0 {
		t.Fatalf("a search cancelled before it started returned %d hits", len(res.Hits))
	}
}
