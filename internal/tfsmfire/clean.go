// internal/tfsmfire/clean.go
//
// CleanOutput, ported from netlapse's ParseEngine._clean_output.
//
// Output that came through netexec is already command-scoped -- echo and
// trailing prompt removed -- and does not need this. It is for text that did
// not: a session transcript pasted into a template tool, a log, a capture from
// another collector.
package tfsmfire

import (
	"regexp"
	"strings"
)

var (
	// hostname + prompt char + optional space + command word.
	promptCommandRe = regexp.MustCompile(`(?i)^[\w\-\.@/]+[#>$)]\s*(show|display|get|dir|bash)\s+`)

	// a line that is only a prompt.
	trailingPromptRe = regexp.MustCompile(`^[\w\-\.@/]+[#>$)]\s*$`)

	preambleRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^terminal\s+(length|width)`),
		regexp.MustCompile(`(?i)^screen.length`),
		regexp.MustCompile(`(?i)^pagination\s+disabled`),
		regexp.MustCompile(`(?i)^set\s+cli\s+screen`),
		regexp.MustCompile(`(?i)^Screen\s+length\s+set`),
		regexp.MustCompile(`(?i)^---\s+JUNOS\s+`),
		regexp.MustCompile(`(?i)^\{master:`),
		regexp.MustCompile(`(?i)^(show|display|get)\s+`),
		regexp.MustCompile(`(?i)^\s*$`),
	}
)

// pyStrip is Python's str.strip(): leading and trailing whitespace.
func pyStrip(s string) string { return strings.TrimSpace(s) }

// CleanOutput strips session preamble and trailing prompts.
//
// Strategy 1: take everything after the LAST line that is a hostname-prefixed
// command echo ("router#show ip arp", "user@switch> show arp"), then drop
// trailing blank and prompt-only lines.
//
// Strategy 2, when there is no such echo: skip known preamble lines at the
// top, drop prompt-only lines anywhere, and trim trailing blank lines.
func CleanOutput(raw string) string {
	lines := strings.Split(raw, "\n")

	last := -1
	for i, line := range lines {
		if promptCommandRe.MatchString(pyStrip(line)) {
			last = i
		}
	}
	if last >= 0 {
		out := append([]string(nil), lines[last+1:]...)
		for len(out) > 0 {
			tail := pyStrip(out[len(out)-1])
			if tail != "" && !trailingPromptRe.MatchString(tail) {
				break
			}
			out = out[:len(out)-1]
		}
		return strings.Join(out, "\n")
	}

	var out []string
	started := false
	for _, line := range lines {
		stripped := pyStrip(line)
		if !started {
			preamble := false
			for _, re := range preambleRes {
				if re.MatchString(stripped) {
					preamble = true
					break
				}
			}
			if preamble {
				continue
			}
			started = true
		}
		if trailingPromptRe.MatchString(stripped) {
			continue
		}
		out = append(out, line)
	}
	for len(out) > 0 && pyStrip(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
