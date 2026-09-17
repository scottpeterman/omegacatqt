#!/usr/bin/env python3
"""scripts/tfsm_re2_rewrite.py

Rewrite templates that Go's regexp engine (RE2) cannot compile into
equivalents it can, in a template database, and prove each rewrite changes
nothing: Python TextFSM must produce identical records from the original and
the rewritten template on the template's own sample output. A rewrite that
fails that check is not applied.

    python3 scripts/tfsm_re2_rewrite.py internal/tfsmfire/seed/tfsm_templates.db

Rewritten rows get source "<source>-re2" and textfsm_hash set to the SHA-256
of the new content, so a later merge from upstream sees them as modified.
The Go side of the proof -- that the rewrite compiles and parses the sample to
the same records -- is internal/tfsmfire's sample test.

Each rewrite is a (template, old, new, why) replacement of one Value regex,
equivalent in the context of the rules that use it.
"""
import hashlib
import io
import json
import sqlite3
import sys

import textfsm

REWRITES = [
    ("arista_eos_show_interfaces_status",
     r"Value NAME (\S.*(?<!\s))", r"Value NAME (\S.*\S|\S)",
     "lookbehind 'does not end in whitespace' -> ends in \\S"),
    ("alcatel_sros_show_service_id_base",
     r"Value SAP_COUNT ([0-9]{1,1500})", r"Value SAP_COUNT ([0-9]+)",
     "RE2 caps a repeat count at 1000"),
    ("cisco_ios_show_ip_ospf_neighbor_detail",
     r"Value LLS_OPTIONS (.+?(?=,))", r"Value LLS_OPTIONS (.+?)",
     "the only rule using it already requires ',' after the value"),
    ("huawei_vrp_display_version",
     r"Value MODEL (((?!\sRouter).)+)", r"Value MODEL (.+?)",
     "tempered token; the rule's \\s+(Router\\s+)?uptime bounds a lazy match"),
    ("fortinet_fnsysctl_ifconfig",
     r"Value MULTICAST (.*(?<!\s))", r"Value MULTICAST (.*\S|)",
     "lookbehind 'does not end in whitespace' -> ends in \\S, or empty"),
    ("mikrotik_routeros_interface_print_detail",
     r"Value List DESCRIPTION ((?!\s*$).+[^\s])", r"Value List DESCRIPTION (.+\S)",
     "a trailing \\S already rules out a whitespace-only remainder"),
    ("cisco_ios_show_lldp_neighbors",
     r"Value Required NEIGHBOR_NAME (.{0,20}(?<! ))", r"Value Required NEIGHBOR_NAME ((?:.{0,19}[^ ])?)",
     "up to 20 characters not ending in a space; empty still allowed at ^"),
]


def records(src, sample):
    f = textfsm.TextFSM(io.StringIO(src))
    return [dict(zip(f.header, row)) for row in f.ParseText(sample)]


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    conn = sqlite3.connect(sys.argv[1])
    failed = 0
    for name, old, new, why in REWRITES:
        row = conn.execute(
            "SELECT rowid, textfsm_content, cli_content, COALESCE(source, '') FROM templates WHERE cli_command = ?",
            (name,)).fetchone()
        if row is None:
            print(f"skip   {name}: not in this database")
            continue
        rowid, src, sample, source = row
        if new in src.splitlines():
            print(f"done   {name}: already rewritten")
            continue
        if src.count(old) != 1:
            print(f"FAIL   {name}: expected the original line exactly once")
            failed += 1
            continue
        rewritten = src.replace(old, new)
        try:
            before, after = records(src, sample or ""), records(rewritten, sample or "")
        except Exception as e:  # noqa: BLE001 -- report and refuse
            print(f"FAIL   {name}: {e}")
            failed += 1
            continue
        if before != after or not before:
            print(f"FAIL   {name}: records differ or sample produced none ({len(before)} vs {len(after)})")
            failed += 1
            continue
        conn.execute(
            "UPDATE templates SET textfsm_content = ?, textfsm_hash = ?, source = ? WHERE rowid = ?",
            (rewritten, hashlib.sha256(rewritten.encode()).hexdigest(),
             source if source.endswith("-re2") else (source or "unknown") + "-re2", rowid))
        print(f"ok     {name}: {len(before)} records identical -- {why}")
    conn.commit()
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
