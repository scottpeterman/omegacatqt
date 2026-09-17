#!/usr/bin/env bash
# scripts/macho-check.sh
#
# Checks that every Mach-O file in a .app bundle can load: each library it
# links is either part of macOS or a file inside the bundle.
#
#   scripts/macho-check.sh OmegaCat.app
#   scripts/macho-check.sh --prune-plugins OmegaCat.app
#
# Exit 0 when everything resolves, 1 otherwise, with each failure listed as
# "file: dependency". With --prune-plugins, a plugin under Contents/PlugIns
# that cannot load is deleted instead of failing -- Qt skips such a plugin at
# run time anyway, so it is dead weight -- and only a failure outside PlugIns
# fails the check.
#
# WHY. macdeployqt copies every plugin in Qt's plugin directory and reports a
# dependency it cannot find as "ERROR:" while exiting 0. With Homebrew's Qt
# that directory is shared by every Qt module installed -- qtpdf's image
# plugin, qtvirtualkeyboard's input plugin -- so a deployment of an app using
# none of them still reports errors. An ERROR line is therefore not the test.
# This is: resolve every dependency the way dyld will.
#
# RESOLUTION, as dyld does it:
#   /usr/lib/..., /System/Library/...  part of macOS
#   @executable_path/...               the app's main executable's directory
#   @loader_path/...                   the directory of the file doing the loading
#   @rpath/...                         each LC_RPATH of that file, then each of
#                                      the main executable's (dyld searches the
#                                      rpaths of the whole chain of loaders, and
#                                      a plugin or framework is loaded by the app)
#   anything else absolute             outside the bundle: a build machine's
#                                      library, so a failure
#
# OTOOL names the otool to use; llvm-otool is output-compatible, which is how
# tests/macho_check_test.sh runs this on Linux.

set -euo pipefail
OTOOL="${OTOOL:-otool}"
PRUNE=0

die() { echo "error: $*" >&2; exit 2; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --prune-plugins) PRUNE=1; shift ;;
        -h|--help) sed -n '3,33p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
        -*) die "unknown option: $1" ;;
        *) break ;;
    esac
done
[[ $# -eq 1 ]] || die "usage: macho-check.sh [--prune-plugins] App.app"
command -v "${OTOOL}" >/dev/null || die "${OTOOL} not found"
APP="$(cd "$1" 2>/dev/null && pwd)" || die "no such bundle: $1"
[[ -d "${APP}/Contents/MacOS" ]] || die "${APP} is not an app bundle (no Contents/MacOS)"

# The main executable: CFBundleExecutable, or the only file in Contents/MacOS.
EXE=""
if [[ -f "${APP}/Contents/Info.plist" ]]; then
    name="$(tr -d '\n\t\r' < "${APP}/Contents/Info.plist" \
        | sed -n 's:.*<key>CFBundleExecutable</key> *<string>\([^<]*\)</string>.*:\1:p')"
    [[ -n "${name}" && -f "${APP}/Contents/MacOS/${name}" ]] && EXE="${APP}/Contents/MacOS/${name}"
fi
if [[ -z "${EXE}" ]]; then
    set -- "${APP}"/Contents/MacOS/*
    [[ $# -eq 1 && -f "$1" ]] || die "cannot tell which file in Contents/MacOS is the executable"
    EXE="$1"
fi
EXE_DIR="$(dirname "${EXE}")"

# LC_RPATH entries of a file, with @loader_path and @executable_path expanded.
rpaths_of() { # file
    local dir; dir="$(dirname "$1")"
    "${OTOOL}" -l "$1" 2>/dev/null | awk '/cmd LC_RPATH/{getline; getline; print $2}' | while IFS= read -r r; do
        r="${r//@loader_path/${dir}}"
        r="${r//@executable_path/${EXE_DIR}}"
        printf '%s\n' "$r"
    done
}
EXE_RPATHS="$(rpaths_of "${EXE}")"

# The dependencies of a file that do not resolve, one per line.
unresolved() { # file
    local f="$1" dir self own dep rest rp ok
    dir="$(dirname "$f")"
    self="$("${OTOOL}" -D "$f" 2>/dev/null | tail -n +2 | head -1 || true)"
    own="$(rpaths_of "$f")"
    "${OTOOL}" -L "$f" 2>/dev/null | tail -n +2 | awk '{print $1}' | while IFS= read -r dep; do
        [[ -z "${dep}" || "${dep}" == "${self}" ]] && continue
        case "${dep}" in
            /usr/lib/*|/System/Library/*) continue ;;
            @executable_path/*) [[ -e "${EXE_DIR}/${dep#@executable_path/}" ]] || echo "${dep}" ;;
            @loader_path/*) [[ -e "${dir}/${dep#@loader_path/}" ]] || echo "${dep}" ;;
            @rpath/*)
                rest="${dep#@rpath/}"
                ok=0
                while IFS= read -r rp; do
                    [[ -n "${rp}" && -e "${rp}/${rest}" ]] && { ok=1; break; }
                done <<<"${own}
${EXE_RPATHS}"
                [[ "${ok}" -eq 1 ]] || echo "${dep}"
                ;;
            *) echo "${dep}  (outside the bundle)" ;;
        esac
    done
}

failures=""
pruned=0
while IFS= read -r -d '' f; do
    file "$f" 2>/dev/null | grep -q 'Mach-O' || continue
    # A file pruned earlier in this loop is gone; find listed it before.
    [[ -e "$f" ]] || continue
    bad="$(unresolved "$f")"
    [[ -z "${bad}" ]] && continue
    rel="${f#${APP}/}"
    if [[ "${PRUNE}" -eq 1 && "${rel}" == Contents/PlugIns/* ]]; then
        echo "removed ${rel}: needs $(echo "${bad}" | head -1 | sed 's/  (outside the bundle)//')"
        rm -f "$f"
        pruned=$((pruned + 1))
        continue
    fi
    while IFS= read -r b; do failures="${failures}${rel}: ${b}"$'\n'; done <<<"${bad}"
done < <(find "${APP}/Contents" -type f -print0)

# A plugin directory emptied by pruning is left out of the bundle.
[[ "${PRUNE}" -eq 1 && -d "${APP}/Contents/PlugIns" ]] && find "${APP}/Contents/PlugIns" -type d -empty -delete

if [[ -n "${failures}" ]]; then
    echo "these cannot load:" >&2
    printf '%s' "${failures}" | sed 's/^/  /' >&2
    exit 1
fi
[[ "${pruned}" -gt 0 ]] && echo "${pruned} plugin(s) removed; everything left resolves" || echo "every dependency resolves"
exit 0