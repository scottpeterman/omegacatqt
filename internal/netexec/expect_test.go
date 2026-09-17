package netexec

import (
	"context"
	"errors"
	"math/rand"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// promptTail must agree with the whole buffer normalized on the one thing
// expect asks of it: the last line. Anything else is either a prompt missed
// (a timeout on a healthy device) or a prompt invented (a capture cut short
// and stored as if whole).
//
// Inputs are built from the fragments real gear emits, including the two
// that break a naive tail: a trailing line of nothing but escapes, and runs
// of blank lines after the prompt. The window is shrunk so almost every
// case exercises the cut and the widening path rather than falling back to
// normalizing everything.
func TestPromptTailAgreesWithFullNormalize(t *testing.T) {
	saved := promptWindow
	t.Cleanup(func() { promptWindow = saved })

	fragments := []string{
		"interface Ethernet1/1\r\n",
		"   description uplink-to-spine-1\r\n",
		" neighbor 192.0.2.1 remote-as 64500\r\r\n",
		"\r\n", "\n", "   \r\n",
		"\x1b[K", "\x1b[2J", "\x1b[0m", "\x1b[7m--More--\x1b[27m",
		"\x1b[24;1H", "\x1b]0;lab-r1\x07",
		"progress 10%\rprogress 50%\rprogress 100%\r\n",
		"\x00", "\x07",
		"lab-r1#", "lab-r1# ", "admin@lab-r1> ",
		"Press any key to continue",
		"x",
	}
	rng := rand.New(rand.NewSource(1))
	for _, window := range []int{1, 2, 7, 16, 64} {
		promptWindow = window
		for i := 0; i < 20000; i++ {
			var b strings.Builder
			for n := rng.Intn(40); n >= 0; n-- {
				b.WriteString(fragments[rng.Intn(len(fragments))])
			}
			buf := []byte(b.String())
			want := lastLine(Normalize(string(buf)))
			if got := lastLine(promptTail(buf)); got != want {
				t.Fatalf("window %d: lastLine(promptTail) = %q, full normalize says %q\nbuf: %q",
					window, got, want, buf)
			}
		}
	}
}

// The case that makes the widening path necessary, named so a regression
// says what it broke.
func TestPromptTailLooksPastAnEscapeOnlyLastLine(t *testing.T) {
	saved := promptWindow
	t.Cleanup(func() { promptWindow = saved })
	promptWindow = 4

	buf := []byte(strings.Repeat("line of config\r\n", 50) + "lab-r1#\r\n\x1b[K\r\n\x1b[0m")
	if got := lastLine(promptTail(buf)); got != "lab-r1#" {
		t.Fatalf("lastLine = %q, want the prompt behind the escape-only lines", got)
	}
}

// feed plays a large output into a session through the real append path, in
// small reads, ending at a prompt — the shape of a running-config over SSH.
//
// Each read waits until expect has taken the previous notification. Over a
// real connection reads arrive at network speed and nearly every one wakes
// expect; a feeder that does not wait races ahead, the wakes coalesce, and
// the quadratic version passes the timing test it exists to fail. (It did,
// the first time this test was written.)
func feed(s *Session, body []byte, chunk int, prompt string) {
	go func() {
		for i := 0; i < len(body); i += chunk {
			end := i + chunk
			if end > len(body) {
				end = len(body)
			}
			s.mu.Lock()
			s.appendLocked(body[i:end])
			s.mu.Unlock()
			s.notify <- struct{}{}
			for len(s.notify) > 0 {
				runtime.Gosched()
			}
		}
		s.mu.Lock()
		s.appendLocked([]byte(prompt))
		s.mu.Unlock()
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}()
}

func testSession(limit int) *Session {
	return &Session{
		prompt:    regexp.MustCompile(DefaultPromptRegex),
		baseLimit: limit,
		limit:     limit,
		notify:    make(chan struct{}, 1),
	}
}

func configBody(size int) []byte {
	line := "   neighbor 192.0.2.1 description transit-peer-example-long-line\r\n"
	return []byte(strings.Repeat(line, size/len(line)))
}

// Regression for the quadratic expect. Before promptTail, 4 MiB in 4 KB
// reads re-normalized the whole buffer on every notification: about 11 s of
// CPU on the machine this was measured on, 2.7 s at 2 MiB. Linear, the same
// read is tens of milliseconds, so the bound here is far below the old cost
// and far above the new one — loose enough for -race and a loaded CI box,
// tight enough that the quadratic version cannot pass it.
//
// The reader goroutine does not wait for expect between chunks, so how many
// notifications expect sees depends on scheduling; the old code was still
// several seconds in practice. The output check matters as much as the time:
// a faster expect that returns a truncated config is the worse bug.
func TestExpectIsLinearInOutputSize(t *testing.T) {
	const size = 4 << 20
	s := testSession(size * 2)
	body := configBody(size)
	feed(s, body, 4<<10, "lab-r1#")

	start := time.Now()
	out, err := s.expect(context.Background(), 30*time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expect: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expect took %v for %d MiB in 4 KB reads; the whole-buffer normalize is back", elapsed, size>>20)
	}
	if want := Normalize(string(body) + "lab-r1#"); out != want {
		t.Fatalf("output differs from the full normalize: got %d bytes, want %d", len(out), len(want))
	}
	if s.lastPrompt != "lab-r1#" {
		t.Errorf("lastPrompt = %q, want lab-r1#", s.lastPrompt)
	}
	if len(s.buf) != 0 {
		t.Errorf("buffer not drained: %d bytes left", len(s.buf))
	}
}

// The overflow path still wins after the change: past the limit the buffer
// is trimmed to its tail, the prompt is still found there, and the caller
// gets ErrOutputTooLarge rather than a partial config.
func TestExpectOverflowStillReportsTooLarge(t *testing.T) {
	s := testSession(64 << 10)
	feed(s, configBody(1<<20), 4<<10, "lab-r1#")

	out, err := s.expect(context.Background(), 30*time.Second)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOutputTooLarge", err)
	}
	if out != "" {
		t.Errorf("returned %d bytes of a truncated capture", len(out))
	}
}

func BenchmarkExpect4MiB4KBReads(b *testing.B) {
	body := configBody(4 << 20)
	for i := 0; i < b.N; i++ {
		s := testSession(8 << 20)
		feed(s, body, 4<<10, "lab-r1#")
		if _, err := s.expect(context.Background(), time.Minute); err != nil {
			b.Fatal(err)
		}
	}
}
