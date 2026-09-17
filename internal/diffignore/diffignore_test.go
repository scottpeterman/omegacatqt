package diffignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsCompileAndMatchTheIOSHeader(t *testing.T) {
	r, err := Parse([]byte(Defaults))
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"cisco_ios", "cisco_iosxe"} {
		for _, line := range []string{
			"Building configuration...",
			"Current configuration : 2406 bytes",
			"! Last configuration change at 05:02:12 UTC Mon Sep 14 2026 by cisco",
			"! NVRAM config last updated at 05:02:20 UTC Mon Sep 14 2026 by cisco",
		} {
			hit := false
			for _, re := range r.For(platform) {
				hit = hit || re.MatchString(line)
			}
			if !hit {
				t.Errorf("%s: %q not ignored", platform, line)
			}
		}
		for _, re := range r.For(platform) {
			if re.MatchString(" description scott was here") || re.MatchString("hostname wan-core-1") {
				t.Errorf("%s: pattern %s matches a real config line", platform, re)
			}
		}
	}
	if len(r.For("arista_eos")) != 0 {
		t.Error("a platform with no rules got some")
	}
}

func TestABadPatternIsSkippedAndTheRestApply(t *testing.T) {
	r, err := Parse([]byte("platforms:\n  cisco_ios:\n    - '(unclosed'\n    - '^Current configuration'\n  '*':\n    - '^! generated'\n"))
	if err == nil || !strings.Contains(err.Error(), "cisco_ios") {
		t.Fatalf("want an error naming the platform, got %v", err)
	}
	if got := len(r.For("cisco_ios")); got != 2 {
		t.Errorf("%d patterns for cisco_ios, want the good one plus \"*\"", got)
	}
}

func TestLoadWritesDefaultsOnceAndReadsEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app", FileName)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != Defaults {
		t.Fatalf("defaults not written: %v", err)
	}
	if err := os.WriteFile(path, []byte("platforms:\n  arista_eos:\n    - '^! device: '\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil || len(r.For("arista_eos")) != 1 || len(r.For("cisco_ios")) != 0 {
		t.Fatalf("edited file not used: %v", err)
	}
}
