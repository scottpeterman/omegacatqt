#!/usr/bin/env python3
"""gen-third-party-notices.py -- regenerate licenses/THIRD_PARTY_NOTICES.md.

    RUN FROM: the repo root (the directory holding go.mod)
    WRITES:   licenses/THIRD_PARTY_NOTICES.md

    python3 scripts/gen-third-party-notices.py           write it
    python3 scripts/gen-third-party-notices.py --check   exit 1 if stale

Ported from omegamaps. The two should stay close: both link the same Go
transport, vault and TextFSM stack behind a C archive, with Qt in front.

BSD-3-Clause, MIT and Apache-2.0 all require the copyright notice and the
warranty disclaimer to be reproduced in the materials distributed with a
binary, and Apache-2.0 section 4(d) additionally requires a NOTICE file to be
reproduced when the work ships one. OmegaCat links every Go dependency below
into a static archive and embeds the template database into that archive with
//go:embed, so the binary carries their code and the obligation travels with it.

The notices file is also compiled into the application (app/resources.qrc) and
shown in Help -> About -> Attributions, so what a user reads is the file this
script wrote, whichever way the program reached them.

WHY THIS IS GENERATED AND NOT WRITTEN BY HAND. A notices file maintained by
hand is correct on the day it is written. This reads the LICENSE and NOTICE
files that shipped inside each module in the local cache, so what lands in the
output is the text the build actually consumed. app_probe then checks that the
copy compiled into the application names every module go.mod requires at the
version go.mod pins, so a dependency bump without a regeneration fails a test
rather than shipping.

WHAT IT ASKS GO FOR. `go list -deps` over ./capi AND ./cmd/... -- not
`go list -m all`, which walks the whole module graph and would claim
obligations for test-only dependencies whose code is never linked. Two roots:
cmd/ocvault reaches golang.org/x/term and the keyring through
internal/vaultcli, which the archive does not.

CANONICAL PATHS, CACHED FILES. `go list` is asked for .Path but for
.Replace.Dir when a replace directive is in force, so a sandbox that mirrors
golang.org/x/crypto through github.com/golang/crypto still reports the module
under its real name while reading the files that were really used.

HAND TABLES. Two kinds of component do not come through the module cache:

  NATIVE  -- Qt, resolved by CMake against whatever the platform provides, and
             the Go runtime, which reports no module. For Qt the linkage is the
             fact the licence turns on, so it is stated. SQLite is here too: it
             arrives inside a Go module, but that module's own licence (MIT-0)
             covers only the translation, and says the original authors'
             licence stays in effect.
  DATA    -- the TextFSM template database, which is data rather than code but
             is embedded and distributed all the same. Its provenance is read
             from the database's own `source` column, so the counts are real;
             the meaning of each label is the table below, and a label with no
             entry stops the script.
"""

import os
import sqlite3
import subprocess
import sys

# Beside the licence texts it points at. This script truncates and rewrites
# OUT, so a mismatch with what is committed does not fail -- it writes a SECOND
# file and leaves the committed one to go stale in place. Every licence path in
# the notes below is therefore relative to this directory.
OUT = os.path.join("licenses", "THIRD_PARTY_NOTICES.md")
PROJECT = "OmegaCat"
TEMPLATE_DB = os.path.join("internal", "tfsmfire", "seed", "tfsm_templates.db")

# What each value of templates.source means. The database records provenance
# per row; this says what the recorded label stands for. A row whose label is
# not a key here stops the script, because a template of unknown origin is the
# one that most needs a person to look at it before it ships.
TEMPLATE_SOURCES = {
    "ntc": {
        "title": "From ntc-templates",
        "note": (
            "From ntc-templates (https://github.com/networktocode/ntc-templates), "
            "Copyright Network to Code, LLC and contributors, licensed under the "
            "Apache License 2.0. The database adds the command name used to "
            "select each template. Full licence text: "
            "`Apache-2.0.txt`, beside this file."
        ),
    },
    "ntc-re2": {
        "title": "From ntc-templates, modified",
        "note": (
            "From ntc-templates, as above, and MODIFIED for this project: the "
            "regular expressions were rewritten by `scripts/tfsm_re2_rewrite.py` "
            "so they compile under RE2 (Go's regexp), which lacks lookaround and "
            "large counted repeats. Each rewrite was accepted only after the "
            "Python TextFSM engine parsed the template's sample identically "
            "before and after. Apache License 2.0; `Apache-2.0.txt`."
        ),
    },
    "chatgpt": {
        "title": "Machine-generated",
        "note": (
            "Generated with ChatGPT for netlapse from sample device output, and "
            "carried into this project with netlapse's template database. Not "
            "taken from a published template collection."
        ),
    },
    "custom": {
        "title": "Written for this project",
        "note": "Written by hand for netlapse and OmegaCat against device output.",
    },
    "": {
        "title": "Written for this project (unlabelled)",
        "note": (
            "Rows with no source label, added by hand for netlapse and OmegaCat "
            "against device output."
        ),
    },
}

# Components that are not Go modules, or whose licence is not the module's.
NATIVE = [
    {
        "name": "Qt 6 (Qt Core, Qt GUI, Qt Widgets, Qt SVG)",
        "holder": "The Qt Company Ltd and other contributors",
        "licence": "GNU Lesser General Public License v3.0",
        "url": "https://www.qt.io/",
        "note": (
            "Dynamically linked against unmodified Qt shared libraries, provided "
            "by the platform or shipped beside the executable; never linked "
            "statically. OmegaCat is conveyed under GPLv3, which LGPLv3 section 3 "
            "expressly permits, and the dynamic link is what lets a recipient "
            "replace Qt with their own build of the same version. Qt's source is "
            "available from "
            "https://download.qt.io/official_releases/qt/ and "
            "https://code.qt.io/. The application's Help menu carries About Qt, "
            "The Qt Company's own notice. Full licence text: `LGPL-3.0.txt` "
            "beside this file, together with `GPL-3.0.txt` which it incorporates "
            "by reference."
        ),
    },
    {
        "name": "Go standard library and runtime",
        "holder": "The Go Authors",
        "licence": "BSD-3-Clause",
        "url": "https://go.dev/",
        "note": (
            "Statically linked into the c-archive and into each command under "
            "`cmd/`, along with the modules below. Listed by hand because the "
            "runtime reports no module in `go list`."
        ),
    },
    {
        "name": "SQLite",
        "holder": "none claimed; the authors dedicated SQLite to the public domain",
        "licence": "Public domain",
        "url": "https://sqlite.org/copyright.html",
        "note": (
            "Compiled in through github.com/ncruces/go-sqlite3-wasm, a machine "
            "translation of SQLite into Go. That module's MIT-0 licence covers "
            "the translation only and states that the original authors' terms "
            "remain in effect, so SQLite is credited here in its own right. "
            "Used for the TextFSM template database."
        ),
    },
]

# Every platform OmegaCat ships for. Build constraints decide what links: the
# keyring backends are github.com/danieljoos/wincred on Windows and
# github.com/godbus/dbus/v5 on Linux, so one GOOS alone is silently short.
TARGETS = [("linux", "amd64"), ("windows", "amd64"), ("darwin", "arm64")]
ROOTS = ["./capi", "./cmd/..."]

LICENCE_NAMES = ("LICENSE", "LICENSE.txt", "LICENSE.md", "LICENCE",
                 "COPYING", "COPYING.txt")
NOTICE_NAMES = ("NOTICE", "NOTICE.txt", "NOTICE.md")


def go_modules():
    """Every module whose code is compiled into any shipped artifact."""
    fmt = ("{{if .Module}}{{.Module.Path}}\t{{.Module.Version}}\t"
           "{{if .Module.Replace}}{{.Module.Replace.Dir}}"
           "{{else}}{{.Module.Dir}}{{end}}{{end}}")
    raw = []
    for goos, goarch in TARGETS:
        # CGO_ENABLED=1, matching the real build: c-archive requires cgo, and
        # with CGO_ENABLED=0 the keyring backends drop out of the answer.
        env = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="1")
        try:
            raw.append(subprocess.check_output(
                ["go", "list", "-deps", "-f", fmt] + ROOTS, text=True, env=env))
        except FileNotFoundError:
            sys.exit("go not on PATH -- this reads the module cache of the "
                     "toolchain that built the archive")
        except subprocess.CalledProcessError as e:
            sys.exit("go list failed for %s/%s (%s). Run `go mod download` "
                     "first." % (goos, goarch, e))

    seen = {}
    for line in "\n".join(raw).splitlines():
        parts = line.split("\t")
        if len(parts) != 3:
            continue
        path, version, directory = parts
        # The standard library reports no module and the main module no
        # version; NATIVE and LICENSE cover those.
        if not path or not version or not directory:
            continue
        seen[path] = (version, directory)
    return sorted((p, v, d) for p, (v, d) in seen.items())


def first_file(directory, names):
    for name in names:
        candidate = os.path.join(directory, name)
        if os.path.isfile(candidate):
            with open(candidate, encoding="utf-8", errors="replace") as fh:
                return name, fh.read().rstrip()
    return None, None


def template_counts():
    """{source label: row count} from the shipped database, read-only."""
    if not os.path.isfile(TEMPLATE_DB):
        sys.exit("%s not found" % TEMPLATE_DB)
    uri = "file:%s?mode=ro" % os.path.abspath(TEMPLATE_DB)
    con = sqlite3.connect(uri, uri=True)
    try:
        rows = con.execute(
            "SELECT COALESCE(source, ''), COUNT(*) FROM templates "
            "GROUP BY COALESCE(source, '')").fetchall()
    finally:
        con.close()
    return dict(rows)


def render():
    counts = template_counts()
    unknown = sorted(k for k in counts if k not in TEMPLATE_SOURCES)
    if unknown:
        sys.exit(
            "the template database has rows whose source label is not in "
            "TEMPLATE_SOURCES: %s\n  Say what the label means before this "
            "database ships." % ", ".join(repr(k) for k in unknown))

    mods = go_modules()
    missing = []
    chunks = []
    for path, version, directory in mods:
        lname, ltext = first_file(directory, LICENCE_NAMES)
        if ltext is None:
            missing.append(path)
            continue
        block = ("### %s\n\nVersion %s. Licence text below is `%s` as shipped "
                 "in the module.\n\n```\n%s\n```\n" % (path, version, lname, ltext))
        nname, ntext = first_file(directory, NOTICE_NAMES)
        if ntext is not None:
            block += ("\nThe module also ships `%s`, reproduced as Apache-2.0 "
                      "section 4(d) requires:\n\n```\n%s\n```\n" % (nname, ntext))
        chunks.append(block)

    out = []
    out.append(
        "# Third-party notices\n\n"
        "%s is Copyright (C) 2026 Scott Peterman and is licensed under the GNU\n"
        "General Public License v3.0; see `LICENSE` at the repository root, or\n"
        "`GPL-3.0.txt` beside this file. It incorporates the components below,\n"
        "whose licences require their copyright notices and warranty\n"
        "disclaimers to be reproduced in the materials distributed with a\n"
        "binary.\n\n"
        "**This file is generated.** Run\n"
        "`python3 scripts/gen-third-party-notices.py` after changing\n"
        "dependencies or the template database; do not edit it by hand.\n\n"
        "## Native components\n\n" % PROJECT)
    for item in NATIVE:
        out.append("### %s\n\n- Copyright: %s\n- %s\n- %s\n\n%s\n\n" % (
            item["name"], item["holder"], item["licence"], item["url"],
            item["note"]))

    total = sum(counts.values())
    out.append(
        "## Embedded data\n\n"
        "### TextFSM template database\n\n"
        "`internal/tfsmfire/seed/tfsm_templates.db`, compiled in by `//go:embed` "
        "and copied to the user's configuration directory on first run. It "
        "holds %d templates. Each row records where it came from; by that "
        "record:\n\n" % total)
    for label in sorted(counts, key=lambda k: (-counts[k], k)):
        src = TEMPLATE_SOURCES[label]
        out.append("- **%s** (%d): %s\n" % (src["title"], counts[label], src["note"]))
    out.append("\n")

    out.append("## Go modules\n\nStatically linked into the c-archive and into "
               "the commands under `cmd/`.\n\n")
    out.append("\n".join(chunks))
    if missing:
        out.append("\n## Licence text not found\n\nThese modules are linked but "
                   "no licence file was found in their cache directory. Resolve "
                   "before distributing.\n\n")
        out.extend("- %s\n" % p for p in missing)
    return "".join(out), len(chunks), missing


def main():
    if not os.path.isfile("go.mod"):
        sys.exit("run this from the repo root -- go.mod not found")
    check = "--check" in sys.argv[1:]

    text, nmods, missing = render()
    if check:
        try:
            with open(OUT, encoding="utf-8") as fh:
                current = fh.read()
        except FileNotFoundError:
            current = None
        if current != text:
            sys.stderr.write("%s is stale; run python3 scripts/"
                             "gen-third-party-notices.py\n" % OUT)
            return 1
        print("%s is current" % OUT)
        return 0

    with open(OUT, "w", encoding="utf-8", newline="\n") as out:
        out.write(text)
    print("wrote %s -- %d Go modules, %d native" % (OUT, nmods, len(NATIVE)))
    if missing:
        print("WARNING: no licence text found for: %s" % ", ".join(missing))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
