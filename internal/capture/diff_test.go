package capture

import (
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"
)

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] > dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	return dp[0][0]
}

// Every script rebuilds both sides, numbers lines correctly, and is minimal:
// its unchanged lines are a longest common subsequence.
func TestEditScriptRebuildsBothSidesAndIsMinimal(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	alphabet := []string{"a", "b", "c", "d"}
	for trial := 0; trial < 400; trial++ {
		a := make([]string, r.Intn(12))
		b := make([]string, r.Intn(12))
		for i := range a {
			a[i] = alphabet[r.Intn(len(alphabet))]
		}
		for i := range b {
			b[i] = alphabet[r.Intn(len(alphabet))]
		}
		ops, err := editScript(a, b)
		if err != nil {
			t.Fatal(err)
		}
		var ra, rb []string
		same := 0
		for _, o := range ops {
			if o.Op != "+" {
				if o.A != len(ra)+1 {
					t.Fatalf("%v -> %v: A numbering off at %+v", a, b, o)
				}
				ra = append(ra, o.Text)
			}
			if o.Op != "-" {
				if o.B != len(rb)+1 {
					t.Fatalf("%v -> %v: B numbering off at %+v", a, b, o)
				}
				rb = append(rb, o.Text)
			}
			if o.Op == "=" {
				same++
			}
		}
		if strings.Join(ra, ",") != strings.Join(a, ",") || strings.Join(rb, ",") != strings.Join(b, ",") {
			t.Fatalf("%v -> %v: rebuilt %v / %v", a, b, ra, rb)
		}
		if want := lcsLen(a, b); same != want {
			t.Fatalf("%v -> %v: %d unchanged lines, LCS is %d", a, b, same, want)
		}
	}
}

func config(lines int, change map[int]string) string {
	var sb strings.Builder
	for i := 1; i <= lines; i++ {
		if s, ok := change[i]; ok {
			if s != "" {
				sb.WriteString(s + "\n")
			}
			continue
		}
		fmt.Fprintf(&sb, "interface GigabitEthernet0/%d\n", i)
	}
	return sb.String()
}

func TestAConfigChangeIsOneHunkWithContext(t *testing.T) {
	older := config(2000, nil)
	newer := config(2000, map[int]string{1000: " description uplink to eng-spine-1"})
	start := time.Now()
	d, err := DiffText([]byte(older), []byte(newer), 3)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("a one-line change in 2000 lines took %v", time.Since(start))
	}
	if d.Added != 1 || d.Removed != 1 || len(d.Hunks) != 1 {
		t.Fatalf("added %d removed %d hunks %d", d.Added, d.Removed, len(d.Hunks))
	}
	h := d.Hunks[0]
	if h.AStart != 997 || h.ALen != 7 || h.BStart != 997 || h.BLen != 7 || len(h.Lines) != 8 {
		t.Errorf("hunk %d,%d %d,%d with %d lines", h.AStart, h.ALen, h.BStart, h.BLen, len(h.Lines))
	}
}

func TestChangesFarApartAreSeparateHunksAndNearOnesMerge(t *testing.T) {
	older := config(100, nil)
	far := config(100, map[int]string{10: "x", 60: "y"})
	near := config(100, map[int]string{10: "x", 15: "y"})
	d, _ := DiffText([]byte(older), []byte(far), 3)
	if len(d.Hunks) != 2 {
		t.Errorf("far changes: %d hunks, want 2", len(d.Hunks))
	}
	d, _ = DiffText([]byte(older), []byte(near), 3)
	if len(d.Hunks) != 1 {
		t.Errorf("changes 5 lines apart with context 3: %d hunks, want 1", len(d.Hunks))
	}
}

func TestIdenticalAndLineEndingsAndTooDifferent(t *testing.T) {
	d, err := DiffText([]byte("a\nb\n"), []byte("a\r\nb"), 3)
	if err != nil || !d.Identical || len(d.Hunks) != 0 {
		t.Errorf("CRLF and a missing final newline are not a change: %+v %v", d, err)
	}
	var older, newer strings.Builder
	for i := 0; i < MaxDiffEdits+10; i++ {
		fmt.Fprintf(&older, "old %d\n", i)
		fmt.Fprintf(&newer, "new %d\n", i)
	}
	if _, err := DiffText([]byte(older.String()), []byte(newer.String()), 3); !errors.Is(err, ErrTooDifferent) {
		t.Errorf("want ErrTooDifferent, got %v", err)
	}
}

func TestStoreDiffRefusesTypesThatAreNotStatic(t *testing.T) {
	st, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Diff("lab-r1", "arp-table", "a.txt", "b.txt", 3); err == nil || !strings.Contains(err.Error(), "not diffed") {
		t.Errorf("got %v", err)
	}
}

// The change from a real IOS box: a description moved from Ethernet3/0 to
// Ethernet3/1. Without rules the header lines make it +3 -3 in two hunks.
func TestIgnoredHeaderLinesAreShownButNotCounted(t *testing.T) {
	head := func(bytes, at string) string {
		return "Building configuration...\n\nCurrent configuration : " + bytes + " bytes\n!\n" +
			"! Last configuration change at " + at + " UTC Mon Sep 14 2026 by cisco\n" +
			"upgrade fpd auto\nversion 15.2\nservice timestamps debug datetime msec\n"
	}
	var body strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&body, "interface Ethernet%d/0\n no ip address\n shutdown\n!\n", i)
	}
	older := head("2406", "05:02:12") + strings.Replace(body.String(), "interface Ethernet3/0\n", "interface Ethernet3/0\n description scott was here\n", 1)
	newer := head("2410", "05:11:13") + strings.Replace(body.String(), "interface Ethernet4/0\n", "interface Ethernet4/0\n description scott was here too\n", 1)

	plain, _ := DiffText([]byte(older), []byte(newer), 3)
	if plain.Added != 3 || plain.Removed != 3 || len(plain.Hunks) != 2 {
		t.Fatalf("precondition: +%d -%d %d hunks, want +3 -3 2", plain.Added, plain.Removed, len(plain.Hunks))
	}

	ignore := []*regexp.Regexp{
		regexp.MustCompile(`^Current configuration : \d+ bytes$`),
		regexp.MustCompile(`^! Last configuration change at `),
	}
	d, err := DiffTextIgnoring([]byte(older), []byte(newer), 3, ignore)
	if err != nil {
		t.Fatal(err)
	}
	if d.Added != 1 || d.Removed != 1 || d.Ignored != 4 || len(d.Hunks) != 1 || d.Identical {
		t.Fatalf("+%d -%d ignored %d, %d hunks; want +1 -1 ignored 4, 1 hunk", d.Added, d.Removed, d.Ignored, len(d.Hunks))
	}
	for _, l := range d.Hunks[0].Lines {
		if strings.HasPrefix(l.Text, "Current configuration") {
			t.Error("the header hunk survived as context of the real change")
		}
	}

	// Only ignored lines differ: not identical, nothing to show.
	d, _ = DiffTextIgnoring([]byte(head("1", "01:00:00")+body.String()), []byte(head("2", "02:00:00")+body.String()), 3, ignore)
	if d.Identical || d.Added != 0 || d.Removed != 0 || d.Ignored != 4 || len(d.Hunks) != 0 {
		t.Errorf("only-ignored: %+v", d)
	}
}
