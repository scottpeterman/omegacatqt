// cmd/capture/main.go
//
// capture — read device state and store it.
//
// A harness, deliberately, not a product surface. It has no features of its
// own: every flag maps onto a capturerun.Params field, and the engine comes
// out of capturedial.Build, which is the same call the window will make. The
// crawl side proved the value of that order — cmd/crawl ran against 83 real
// devices before crawlrun existed, so when the window found a bug it was
// identifiably a window bug. Reverse it and a blank column has three equally
// good explanations.
//
// Nothing here writes to a device. Every command comes from a capture spec and
// every spec command is on the read-only allowlist the spec tests enforce.
//
// Example (lab):
//
//	capture -device lab-r1.lab.example -user admin -password \
//	        -store ~/captures -type running-config
//
//	capture -device-file devices.txt -vault ~/.omegacat/vault.json \
//	        -cred-tag lab -store ~/captures \
//	        -type running-config -type inventory -v
//
// A device list is a text file, one device per line, # for comments. The
// commented-out line explaining why a box is off the list is the reason the
// parser honours comments rather than stripping them.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/scottpeterman/omegacatqt/internal/buildinfo"
	"github.com/scottpeterman/omegacatqt/internal/capture"
	"github.com/scottpeterman/omegacatqt/internal/capturedial"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/sshcore"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// expandCSV splits comma-separable repeatable flag values.
func expandCSV(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func parseJump(spec, fallbackUser string) (*sshcore.JumpConfig, error) {
	j := &sshcore.JumpConfig{Username: fallbackUser}
	rest := spec
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		j.Username = rest[:i]
		rest = rest[i+1:]
	}
	if host, portStr, err := net.SplitHostPort(rest); err == nil {
		p, perr := strconv.Atoi(portStr)
		if perr != nil {
			return nil, fmt.Errorf("jump port %q: %v", portStr, perr)
		}
		j.Host, j.Port = host, p
	} else {
		j.Host = rest
	}
	if j.Host == "" {
		return nil, fmt.Errorf("jump spec %q has no host", spec)
	}
	return j, nil
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "capture: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var devices, types, domains, credTags, match stringList

	flag.Var(&devices, "device", "device to capture (repeatable or comma-separated)")
	deviceFile := flag.String("device-file", "", "file of devices, one per line, # for comments")
	sessionFile := flag.String("sessions", "", "session inventory to select devices from; needs at least one -match")
	flag.Var(&match, "match", "glob selecting sessions out of -sessions by name or host: 'agg*', '*-sw-*' (repeatable or comma-separated; '*' is all of them)")
	flag.Var(&types, "type", "capture type (repeatable or comma-separated); default running-config")
	flag.Var(&domains, "domain", "domain suffix (repeatable): stripped when deriving a device identity")
	store := flag.String("store", "", "capture store root directory")

	user := flag.String("user", os.Getenv("USER"), "ssh username")
	keyPath := flag.String("key", "", "private key path")
	askPass := flag.Bool("password", false, "prompt for a password")
	jumpSpec := flag.String("jump", "", "jump host: [user@]host[:port]")
	jumpKey := flag.String("jump-key", "", "jump host private key path")
	legacy := flag.Bool("legacy", false, "enable legacy KEX/ciphers")
	noParse := flag.Bool("no-parse", false, "store ARP and MAC tables without parsing them")
	templates := flag.String("templates", "", "TextFSM template database (default ~/.omegacat/tfsm_templates.db, created on first use)")

	vaultPath := flag.String("vault", "", "credential vault path; enables per-device credential resolution (overrides -user/-key/-password)")
	bindingsPath := flag.String("bindings", "", "credential binding store path (default: alongside the vault)")
	flag.Var(&credTags, "cred-tag", "only offer credentials carrying all of these tags (repeatable or comma-separated)")

	tofu := flag.Bool("tofu", false, "trust unknown host keys on first contact and record them; a key that CHANGED still fails closed")
	knownHosts := flag.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")

	conc := flag.Int("concurrency", 5, "devices visited at once")
	expConc := flag.Int("expensive-concurrency", 1, "concurrent expensive commands across the whole run")
	timeout := flag.Duration("timeout", 60*time.Second, "default per-command timeout; a spec's own bound wins over it")

	listTypes := flag.Bool("list-types", false, "print the known capture types and exit")
	dryRun := flag.Bool("dry-run", false, "resolve and print what would be captured, then exit without connecting")
	verbose := flag.Bool("v", false, "verbose progress")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Line("capture"))
		return
	}

	if *listTypes {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, s := range capture.Builtin() {
			fmt.Fprintf(w, "%s\t%s\t%s\n", s.Type, s.Description,
				strings.Join(s.Platforms(), ","))
		}
		w.Flush()
		return
	}

	p := capturerun.Defaults()
	p.Devices = expandCSV(devices)
	p.DeviceFile = *deviceFile
	p.SessionFile = *sessionFile
	p.Match = expandCSV(match)
	p.Types = expandCSV(types)
	p.Domains = expandCSV(domains)
	p.StorePath = *store
	p.Concurrency = *conc
	p.ExpensiveConcurrency = *expConc
	p.Timeout = *timeout
	p.VaultPath = *vaultPath
	p.CredTags = expandCSV(credTags)
	p.KnownHostsPath = *knownHosts
	p.Legacy = *legacy
	p.NoParse = *noParse
	p.TemplatesPath = *templates
	if *tofu {
		p.HostKeys = capturerun.HostKeyTOFU
	}

	var password string
	if *askPass {
		fmt.Fprint(os.Stderr, "password: ")
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			die("reading password: %v", err)
		}
		password = string(b)
	}

	var jump *sshcore.JumpConfig
	if *jumpSpec != "" {
		j, err := parseJump(*jumpSpec, *user)
		if err != nil {
			die("%v", err)
		}
		j.PrivateKeyPath = *jumpKey
		j.Password = password
		jump = j
	}

	logf := func(string, ...any) {}
	if *verbose {
		logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}

	run := capturerun.New()
	built, err := capturedial.Build(p, capturedial.Options{
		Static: capturedial.StaticCreds{
			Username: *user, Password: password, KeyPath: *keyPath,
		},
		Jump:         jump,
		BindingsPath: *bindingsPath,
		Log:          logf,
		CredLog:      logf,
		Emit:         run.Emit(),
	})
	if err != nil {
		die("%v", err)
	}
	defer built.Close()

	printPlan(built, p)
	if *dryRun {
		return
	}

	// Ctrl-C cancels the run rather than killing it. Devices already in
	// flight drain; queued ones fall through and are reported as failed
	// with a reason, never dropped.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	results := built.Engine.Capture(ctx, built.Devices)
	run.Finish()

	printResults(run, results)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "capture: interrupted; the run above is partial")
	}
	if run.Counts().Failed > 0 {
		os.Exit(1)
	}
}

// printPlan says what is about to happen before anything connects. A capture
// is the one operation where being told afterwards is too late — the argument
// that was wrong has already been applied to every device on the list.
func printPlan(b *capturedial.Built, p capturerun.Params) {
	specTypes := make([]string, 0, len(b.Specs))
	for _, s := range b.Specs {
		specTypes = append(specTypes, s.Type)
	}
	fmt.Printf("capture: %d device(s) x %d type(s) [%s] -> %s\n",
		len(b.Devices), len(b.Specs), strings.Join(specTypes, ", "), b.Store.Root())

	// Sessions a pattern matched that capture cannot visit. Printed before
	// the identity notes because "fourteen matched, nine captured" is the
	// first question a pattern raises, and answering it after the run
	// means answering it after the wrong set was captured.
	if lines := capturedial.SkippedLines(b.Skipped); len(lines) > 0 {
		fmt.Printf("  %d matched session(s) skipped:\n", len(lines))
		for _, l := range lines {
			fmt.Println("    " + l)
		}
	}

	if len(b.Notes) == 0 {
		return
	}
	// Identity decisions are worth showing before the run, not after: a
	// CGNAT address that did not become a name is about to file a config
	// under an address that shared space recycles.
	ids := make([]string, 0, len(b.Notes))
	for id := range b.Notes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %s: %s\n", id, b.Notes[id])
	}
}

func printResults(run *capturerun.Run, results []capture.Result) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE\tTYPE\tSTATE\tPLATFORM\tBYTES\tTIME\tPARSED\tDETAIL")
	for _, r := range run.RowsSorted() {
		name := r.Name
		if name == "" {
			name = r.Identity
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			name, r.Type, r.State, r.Platform, r.Bytes,
			r.Duration().Round(time.Millisecond), parseCell(r), r.Detail)
	}
	w.Flush()

	if notable := run.Decisions(); len(notable) > 0 {
		fmt.Println("\ndecisions:")
		for _, ev := range notable {
			fmt.Println("  " + ev.Describe())
		}
	}

	c := run.Counts()
	fmt.Printf("\n%d device(s), %d capture(s) in %s: %d stored (%d bytes), "+
		"%d unchanged, %d not applicable, %d failed\n",
		c.Devices, len(results), run.Elapsed().Round(time.Millisecond),
		c.Stored, c.BytesStored, c.Unchanged, c.NotApplicable, c.Failed)
	if c.NewHostKeys > 0 {
		fmt.Printf("%d host key(s) trusted on first contact\n", c.NewHostKeys)
	}
}

// parseCell is the PARSED column: the rows and template for a parse, how close
// the best template came for a miss, and blank for a type that is not parsed.
func parseCell(r capturerun.Row) string {
	switch r.ParseStatus {
	case "":
		return ""
	case "parsed":
		return fmt.Sprintf("%d rows, %s (%.1f)", r.ParseRecords, r.Template, r.ParseScore)
	default:
		if r.Template == "" {
			return "no template matched"
		}
		return fmt.Sprintf("no match, best %s (%.1f)", r.Template, r.ParseScore)
	}
}
