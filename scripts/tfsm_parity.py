#!/usr/bin/env python3
"""scripts/tfsm_parity.py

Record the Python tfsm-fire engine's selections, so the Go port can be held to
them. For every template whose sample output is stored in the database, and for
each hint given, run find_best_template(sample, hint) and record the winner and
its exact score.

    python3 scripts/tfsm_parity.py path/to/tfsm_fire.py path/to/tfsm_templates.db \
        arista_eos juniper_junos arista_eos_show_mac > internal/tfsmfire/testdata/parity.json

A hint selects which samples are run as well as which templates compete: the
samples are those of the templates the hint's platform prefix owns (the first
two '_' terms, or the whole hint when shorter).

Scores are written with repr, which round-trips a float64 exactly, so the Go
test can compare for equality rather than within a tolerance.

Needs textfsm; tfsm_fire.py's click dependency is stubbed.
"""
import importlib.util
import json
import sqlite3
import sys
import types

if len(sys.argv) < 4:
    sys.exit(__doc__)
fire_path, db_path, hints = sys.argv[1], sys.argv[2], sys.argv[3:]

click = types.ModuleType("click")
click.echo = lambda *a, **k: None
click.style = lambda s, **k: s
sys.modules.setdefault("click", click)

spec = importlib.util.spec_from_file_location("tfsm_fire", fire_path)
fire = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fire)
engine = fire.TextFSMAutoEngine(db_path)
conn = sqlite3.connect(db_path)

cases = []
for hint in hints:
    prefix = "_".join(hint.split("_")[:2])
    rows = conn.execute(
        "SELECT cli_command, cli_content FROM templates WHERE cli_command LIKE ? ORDER BY rowid",
        (prefix + "_%",),
    ).fetchall()
    for name, sample in rows:
        if not sample or not sample.strip():
            continue
        winner, records, score = engine.find_best_template(sample, hint)
        cases.append({
            "sample": name,
            "hint": hint,
            "winner": winner or "",
            "score": repr(float(score)),
            "records": len(records or []),
        })

json.dump({"db_rows": conn.execute("SELECT COUNT(*) FROM templates").fetchone()[0],
           "cases": cases}, sys.stdout, indent=1)
sys.stdout.write("\n")
