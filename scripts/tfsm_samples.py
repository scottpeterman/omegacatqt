#!/usr/bin/env python3
"""scripts/tfsm_samples.py

Record what Python TextFSM parses from every template's own sample output, as
a digest per template, so the Go engine can be held to it without shipping
megabytes of expected records.

    python3 scripts/tfsm_samples.py internal/tfsmfire/seed/tfsm_templates.db \
        > internal/tfsmfire/testdata/samples.json

The digest is SHA-256 over canonical JSON of the records: keys sorted, no
whitespace, UTF-8 unescaped. The Go test builds the same bytes.
"""
import hashlib
import io
import json
import sqlite3
import sys

import textfsm

conn = sqlite3.connect(sys.argv[1])
out = {}
for name, src, sample in conn.execute(
        "SELECT cli_command, textfsm_content, cli_content FROM templates ORDER BY rowid"):
    if not sample or not sample.strip():
        continue
    try:
        f = textfsm.TextFSM(io.StringIO(src))
        recs = [dict(zip(f.header, row)) for row in f.ParseText(sample)]
    except Exception as e:  # noqa: BLE001
        out[name] = {"error": str(e)[:200]}
        continue
    canon = json.dumps(recs, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    out[name] = {"records": len(recs), "sha256": hashlib.sha256(canon.encode()).hexdigest()}
json.dump(out, sys.stdout, indent=0, sort_keys=True)
sys.stdout.write("\n")
