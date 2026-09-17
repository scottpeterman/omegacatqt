#!/usr/bin/env bash
# scripts/scrub-check.sh — fail if anything on a private denylist is in the tree.
#
# The denylist is NOT in this repository and must never be: a committed list
# of the names, site codes and address ranges that must not appear is itself
# the leak. It lives outside the tree, one extended regex per line, # for
# comments, matched case-insensitively:
#
#   ${OMEGACAT_DENYLIST:-$HOME/.config/omegacat/denylist}
#
# Run it before every push, and before anything that makes the repo public.
#
#   scripts/scrub-check.sh            scan the working tree
#   scripts/scrub-check.sh --staged   scan only what is staged (pre-commit)
#
# Exit status: 0 clean, 1 matches found, 2 no denylist.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
list="${OMEGACAT_DENYLIST:-$HOME/.config/omegacat/denylist}"

if [[ ! -r "$list" ]]; then
	echo "scrub-check: no denylist at $list (set OMEGACAT_DENYLIST)" >&2
	exit 2
fi

patterns="$(mktemp)"
trap 'rm -f "$patterns"' EXIT
grep -vE '^[[:space:]]*(#|$)' "$list" >"$patterns" || true
if [[ ! -s "$patterns" ]]; then
	echo "scrub-check: $list has no patterns" >&2
	exit 2
fi

cd "$root"
if [[ "${1:-}" == "--staged" ]]; then
	mapfile -t files < <(git diff --cached --name-only --diff-filter=ACMR)
else
	mapfile -t files < <(
		find . -type f \
			-not -path './.git/*' \
			-not -path './build/*' \
			-not -path './dist/*' \
			-not -path './bin/*' \
			-not -name '*.png' -not -name '*.ico' -not -name '*.icns' \
			-not -name '*.gif' -not -name '*.jpg' -not -name '*.zip' \
			-not -name '*.db-journal' -not -name '*.db-wal' -not -name '*.db-shm' |
			sed 's|^\./||'
	)
fi

if [[ ${#files[@]} -eq 0 ]]; then
	echo "scrub-check: nothing to scan"
	exit 0
fi

# SQLite files are scanned through a text dump. grep -I treats them as binary
# and skips them, so a template database carrying sample output captured from
# production gear would otherwise pass this check without a word. A database
# that cannot be dumped fails the check rather than being skipped.
found=0
text_files=()
for f in "${files[@]}"; do
	[[ -f "$f" ]] || continue
	if [[ "$(head -c 15 "$f" 2>/dev/null)" == "SQLite format 3" ]]; then
		if ! command -v sqlite3 >/dev/null; then
			echo "scrub-check: $f is a SQLite database and sqlite3 is not installed to scan it" >&2
			exit 2
		fi
		if ! dump="$(sqlite3 "$f" .dump 2>&1)"; then
			echo "scrub-check: cannot dump $f: $dump" >&2
			exit 2
		fi
		if hits="$(printf '%s\n' "$dump" | grep -niE -f "$patterns")"; then
			echo "scrub-check: denylisted content in database $f:" >&2
			printf '%s\n' "$hits" | cut -c1-240 | sed "s|^|$f (dump) :|" >&2
			found=1
		fi
		continue
	fi
	text_files+=("$f")
done

# -I skips binary files; -n and -H give file:line for every hit.
if [[ ${#text_files[@]} -gt 0 ]] && hits="$(grep -InHiE -f "$patterns" -- "${text_files[@]}" 2>/dev/null)"; then
	echo "scrub-check: denylisted content found:" >&2
	echo "$hits" >&2
	found=1
fi
if [[ $found -ne 0 ]]; then
	exit 1
fi
echo "scrub-check: clean (${#files[@]} files)"
