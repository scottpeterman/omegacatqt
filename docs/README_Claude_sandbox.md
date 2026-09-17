# README_Claude_sandbox.md

Recipe for building and testing OmegaCat in a Claude sandbox, so changes get
**built, run and tested** there rather than reasoned about from the source.

omegamaps (`docs/README_Claude_sandbox.md`) and omegasshqt
(`docs/README_Claude_Qt_sandbox.md`) have the same document. This one follows
them, with OmegaCat's own differences: the shipping Go toolchain (1.25) runs
in the sandbox instead of a floor build, `go.mod` is never edited (a
`-modfile` copy carries the mirrors), and there are two things neither of the
others has -- a TextFSM engine held to a Python original, and fake devices
that a live capture can run against with no privileges.

Verified end to end on Ubuntu 24.04 with Go 1.25.14, CMake 3.28.3, Qt 6.4.2,
Python 3.12 and textfsm 2.1.0.

---

## 0. What the sandbox is

A Linux container (Ubuntu 24.04, root, **one CPU**) with a shell and a file
system. Its properties decide what can be proved in it:

- **Network egress is an allowlist.** GitHub, `raw.githubusercontent.com`,
  PyPI, npm and the Ubuntu archives are reachable. The Go module proxy,
  `sum.golang.org`, `golang.org`, `gopkg.in`, `go.dev` and `modernc.org` are
  not (section 2).
- **Every tool call is a fresh `/bin/sh` (dash) with a 300-second limit.**
  Environment variables do not carry between calls, and **background
  processes die when the call that started them returns** -- `nohup` does
  not save them. Anything a test needs running is started in the same call
  (section 8).
- **Files arrive as uploads or GitHub clones and leave as a zip.** The
  sandbox can be reset between sessions; nothing in it is the source of truth.
- **No terminal, no OS keyring, no display, no network gear, no macOS or
  Windows, and it is amd64.** Section 10 is what that leaves unproved.

---

## 1. Toolchain

```bash
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y cmake qt6-base-dev qt6-svg-dev sqlite3
pip install --break-system-packages textfsm pillow
```

`qt6-base-dev` brings Widgets and the offscreen platform plugin; Ubuntu's Qt
is 6.4.2, above the 6.2 floor. `sqlite3` is for `scripts/scrub-check.sh`,
which scans inside the template database (section 7). `textfsm` is for the
Python side of the template parity tests (section 5); `pillow` only for
cropping screenshots to look at. g++ is already in the image.

**Go: the apt package (1.22) is too old.** `go.mod` says 1.25.0 and the SQLite
driver requires it, and `go.dev/dl` is unreachable. The GitHub Actions Go
builds are reachable, through a manifest on `raw.githubusercontent.com`:

```bash
URL=$(curl -s https://raw.githubusercontent.com/actions/go-versions/main/versions-manifest.json \
  | grep -o '"download_url": *"[^"]*go-1\.25\.[0-9]*-linux-x64.tar.gz"' | head -1 | cut -d'"' -f4)
curl -sL -o /tmp/go.tgz "$URL" && mkdir -p /opt/go125 && tar xzf /tmp/go.tgz -C /opt/go125
/opt/go125/bin/go version
```

Put `/opt/go125/bin` first on `PATH` in every call. If the apt Go is also
installed, CMake must be told which Go to use (section 4).

## 2. The Go module proxy is unreachable

`golang.org/x/*`, `gopkg.in/yaml.v3` and `gopkg.in/check.v1` are fetched from
their GitHub mirrors. The mirrors go in a **copy** of `go.mod`, and every Go
command is pointed at the copy -- the working tree's `go.mod` and `go.sum` are
never changed, so there is nothing to revert before delivering:

```bash
export PATH=/opt/go125/bin:$PATH
eval "$(scripts/sandbox-modfile.sh)"
```

That writes `/tmp/omegacat-dev.mod` (replace block, mirror versions read from
`go.mod`) and `/tmp/omegacat-dev.sum`, and exports:

```bash
GOTOOLCHAIN=local GOPROXY=direct GOSUMDB=off GOFLAGS="-mod=mod -modfile=/tmp/omegacat-dev.mod"
```

Both lines go at the top of **every** tool call that runs Go, CMake or a build
script. A call without them fails with `missing go.sum entry` or tries to
download a toolchain.

Things learned the hard way:

- **A mirror has to match the version `go.mod` pins.** A replace naming
  `golang.org/x/sys v0.43.0` while `go.mod` requires `v0.45.0` builds, but
  against the older code. The script reads the versions from `go.mod` for
  that reason; a new `golang.org/x` module needs one line in its table.
- **`go get` for a new dependency has to run with the modfile env**, and then
  the real `go.mod` has to be edited to match (copy the `require` lines, not
  the `replace` block). Adding the SQLite driver only to the copy shipped a
  `go.mod` without it; `go mod tidy` on a real machine then picked the newest
  driver, which needs Go 1.26, and the go command quietly downloaded 1.26.
  Pin the version in the real `go.mod`, and check its `go` directive
  (`curl -s https://raw.githubusercontent.com/<owner>/<repo>/<tag>/go.mod`)
  before choosing it.
- **The sandbox cannot produce the shipping `go.sum`** for `golang.org/x`
  modules (the mirror's module hashes are recorded under the mirror's path).
  `scripts/build.sh` runs `go mod tidy` on a real machine when `go.sum` is
  missing entries, and says so; that is the fix, not a sandbox one.
- **`modernc.org/sqlite` is unreachable** (its host is blocked), which is why
  the template engine uses `github.com/ncruces/go-sqlite3` -- also pure Go, so
  the tools still build with `CGO_ENABLED=0`.

## 3. Go build and tests

```bash
export PATH=/opt/go125/bin:$PATH; eval "$(scripts/sandbox-modfile.sh)"
gofmt -l .                                   # must print nothing
go vet ./... && CGO_ENABLED=1 go vet ./capi
go test -race -count=1 $(go list ./... | grep -v tfsmfire)
go test -count=1 ./internal/tfsmfire         # corpus parity, ~40 s, not under -race
```

**Split the test run.** The whole suite under `-race` in one call runs past
the 300-second limit on one CPU, and a call that times out returns no output
at all -- not a partial log. The template engine's corpus tests skip
themselves under `-race` (minutes there, and no concurrency in them); its
concurrency test still runs raced.

A command that can run long should write to a file and print the file:
`(go test ... > /tmp/t.log 2>&1; echo "exit $?" >> /tmp/t.log); grep -v '^ok' /tmp/t.log`.

**Prove a test catches its bug.** A test that passes on the first run proves
the code works or the test is blind. Break the code on purpose (revert the
fix, stub the check) and confirm the test fails. Several tests here were
blind until this was done: a timing test whose feeder raced ahead of the code
under test and let the quadratic version pass, and a screenshot colour check
that sampled the wrong pixel.

## 4. C surface and the Qt application

```bash
export PATH=/opt/go125/bin:$PATH; eval "$(scripts/sandbox-modfile.sh)"
cmake -S . -B /tmp/qbuild -DCMAKE_BUILD_TYPE=Release -DGO_EXECUTABLE=/opt/go125/bin/go
cmake --build /tmp/qbuild -j4 2>&1 | grep -E 'error|warning:'
ctest --test-dir /tmp/qbuild --output-on-failure      # capi_probe, app_probe
```

`scripts/build-app.sh --build-dir /tmp/bapp --probe` does the same with Qt
discovery, and works here unchanged.

- **The env goes in the same call as `cmake --build`.** The Go c-archive is a
  custom command that inherits the build's environment.
- **`-DGO_EXECUTABLE` matters when the apt Go is installed.** Given a Qt
  prefix of `/usr`, `find_program(go)` used to find `/usr/bin/go` (1.22)
  before `PATH`. The CMake file now skips `CMAKE_PREFIX_PATH` for Go and the
  scripts pass the Go on `PATH`, but pass it explicitly here anyway.
- **The stale-binary trap.** If the archive step fails, nothing C++ relinks
  and the old binaries in the build directory still run. When a change seems
  to do nothing, check the binary's timestamp and look for
  `Building Go c-archive` in the build output.

**capi_probe** (plain C) drives the whole C surface: vault create/store and
the SNMP refusal, the demo run, a live capture of the fake devices twice
(stored, then unchanged under strict host keys), the store, `store_parsed`,
search, and path traversal refused. **app_probe** drives the real window
offscreen: the demo through the Run view, a vault in a directory that does not
exist yet, a metadata edit through the credential editor followed by a capture
that only authenticates if the password survived, the credential manager and
unlock dialog, a live capture, the Store view's parsed ARP table, and a search.
Both take the shipped template database read-only from the source tree, never
`~/.omegacat`.

**Look at the grabs, not just the exit status.** app_probe saves a screenshot
of each view in each theme:

```bash
QT_QPA_PLATFORM=offscreen /tmp/qbuild/tests/app_probe /tmp/shots/omegacat \
    /tmp/qbuild/tests/fakedevice internal/tfsmfire/seed/tfsm_templates.db
```

then view `/tmp/shots/omegacat-{run,store,search,credentials}-*.png` with the
image viewer. Every check passed while the grabs showed: the results table's
DETAIL column pushed off-screen, a decisions list scrolling sideways, a search
highlight unreadable after a theme change, and the Omega credential dialogs
with a light grey body on the dark theme (widgets that autofill use the
palette, not the stylesheet). Each became an assertion after it was seen.
`propagateSizeHints()` and `XDG_RUNTIME_DIR not set` warnings are noise.

The offscreen style differs from a real desktop: unchecked list-item
checkboxes were visible here and invisible on a Linux desktop palette. A grab
proves layout and colour roles, not the desktop's own style.

## 5. The TextFSM engine and its Python original

`internal/tfsmfire` is a port of netlapse's `tfsm_fire.py`, required to choose
the same template with the bit-identical score. The golden files come from the
Python engine:

```bash
python3 scripts/tfsm_parity.py path/to/tfsm_fire.py internal/tfsmfire/seed/tfsm_templates.db \
    arista_eos juniper_junos arista juniper arista_eos_show_mac juniper_junos_show_ethernet \
    > internal/tfsmfire/testdata/parity.json
python3 scripts/tfsm_samples.py internal/tfsmfire/seed/tfsm_templates.db \
    > internal/tfsmfire/testdata/samples.json
```

`tfsm_fire.py` is not in this repository (it is netlapse's); upload it for a
regeneration. Regenerate after any change to the seed database.

- **Python `find_best_template` recompiles every candidate template per
  call.** A vendor-wide pass over Cisco (264 templates against 264 samples)
  does not finish inside a tool call on one CPU, and backgrounding it does
  not help (section 0). Measure broad selection accuracy with the Go engine,
  which compiles each once; keep Python runs to the hints in the golden set.
- **Hints.** A platform hint (`arista_eos`) is the normal case; a command stem
  (`arista_eos_show_mac`) is for commands with version variants. A hint that
  names exactly one template leaves nothing to score. Terms of two characters
  or fewer are dropped, so `cisco_xr` is just `cisco`.
- **RE2 cannot compile everything Python can** (lookaround, repeats over 1000,
  named groups inside a Value). `scripts/tfsm_re2_rewrite.py` rewrites a
  template only after proving Python parses its sample identically before and
  after. The ones left are listed with reasons in `samples_test.go`.
- **Floating point.** The sandbox is amd64, where Go does not fuse `x*y+z`;
  arm64 does, and the first Apple Silicon build scored one template
  `84.72222222222221` against Python's `…223`. Every product that reaches a
  sum in `score.go` is wrapped in `float64()`. To check without arm64
  hardware, cross-compile and count fused instructions:

  ```bash
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/fma ./some-main-using-tfsmfire
  go tool objdump -s 'tfsmfire.Score' /tmp/fma | grep -ciE 'FMADD|FMSUB|FNMADD|FNMSUB'   # must be 0
  ```

## 6. Vault, CLI and live captures end to end

No terminal and no keyring, so secrets are piped and the keyring is bypassed.
`tests/fakedevice` runs an IOS and an EOS device on loopback ports with no
privileges and exits when its stdout closes:

```bash
export PATH=/opt/go125/bin:$PATH; eval "$(scripts/sandbox-modfile.sh)"
go build -o /tmp/e2e/bin/ ./cmd/capture ./cmd/ocvault ./tests/fakedevice
export OMEGACAT_VAULT_PASSWORD=lab-master-pw OMEGACAT_NO_KEYRING=1 HOME=/tmp/e2e/home
mkdir -p $HOME
/tmp/e2e/bin/fakedevice > /tmp/e2e/ready.txt &
sleep 1
ADDR=$(python3 -c "import json;print(json.loads(open('/tmp/e2e/ready.txt').readline())['devices'][0]['addr'])")
printf 'lab-master-pw\nlab-master-pw\n' | /tmp/e2e/bin/ocvault init
echo lab-password | /tmp/e2e/bin/ocvault add -name lab -user admin -tag lab
/tmp/e2e/bin/capture -device "$ADDR" -vault $HOME/.omegacat/vault.json -cred-tag lab \
    -store /tmp/e2e/store -type running-config,arp-table -tofu -known-hosts /tmp/e2e/kh \
    -templates internal/tfsmfire/seed/tfsm_templates.db
```

All of it in one call: the fake device dies with the call that started it.
Run the capture twice; the second should report `2 unchanged` without `-tofu`.
The throwaway `HOME` exercises the default `~/.omegacat` path, including
creating the directory, without touching anything real -- the first GUI run on
a real laptop failed exactly there, because only `ocvault` had ever created it.

## 7. Scrub before anything leaves

```bash
OMEGACAT_DENYLIST=/path/to/denylist scripts/scrub-check.sh
```

The denylist lives outside the repository and is never committed (a list of
the names that must not appear is itself the leak); in a sandbox it is written
to `/tmp` for the session. The check dumps SQLite files and scans their text,
because `grep -I` skips a database as binary -- and the template database is
exactly where captured production output would land. It fails, not skips,
when `sqlite3` is missing.

Examples and tests use lab names only (`lab-r1`, `wan-core-1`, `172.16.x.x`,
`lab.local`). Output from real gear pasted into a session is for reading, never
for fixtures or the seed database.

## 8. The tool shell

- **dash:** no `time` (use `date +%s` arithmetic), no `<(...)`, no `${PIPESTATUS}`.
  Piping into `grep` reports grep's status; capture `$?` first.
- **300 seconds per call, and a timeout loses all output.** Long work writes to
  a file.
- **Background processes do not outlive the call.** A Python pass started with
  `nohup ... &` in one call was gone, with no output file, by the next.
- **`pkill -f <pattern>`** can match the tool shell's own command line and kill
  the call; kill by PID (`$!`).
- **Heredocs with CRLF files.** `open()` in Python translates line endings on
  read; edit `scripts/build.bat` with `newline=''` or the replacement silently
  matches nothing.

## 9. Delivering

```bash
gofmt -l .                                            # nothing
OMEGACAT_DENYLIST=/tmp/denylist scripts/scrub-check.sh  # clean
cd .. && zip -qr omegacatqt.zip omegacatqt -x 'omegacatqt/build/*'
```

`go.mod` and `go.sum` in the tree are the shipping ones (section 2 never edits
them). When a dependency changed, say so: the first `build.sh` on a real
machine will run `go mod tidy` to add the checksums the sandbox cannot.

## 10. What the sandbox cannot tell you

- **No macOS, no Windows, no arm64.** Cross-compiles prove the tools build.
  The c-archive's darwin framework links, the MSVC seams, `scripts/build.bat`
  and arm64 floating point were each first exercised on a real machine; the
  arm64 score difference and a Go 1.26 toolchain download were found there.
- **No display.** The application runs offscreen: layout, colour roles, data
  flow and dialogs are proved; mouse input, native file dialogs, the desktop
  style and a real window manager are not.
- **No OS keyring.** Keyring unlock and "remember this password" are untested
  here.
- **No real gear.** Fake devices prove the engine, identity, parsing and the
  store, and they earn their keep: capi_probe's first live run found devices
  filed under their dial address instead of their prompt name, and two
  port-forwarded devices merged into one. What they cannot show is real
  output -- IOS, EOS and Junos QFX captures and their parses were validated
  on the user's lab and devices, not here.
- **Not the shipping `go.sum`.** Checksums for `golang.org/x` come from a real
  machine's `go mod tidy`.
