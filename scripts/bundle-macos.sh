#!/usr/bin/env bash
# scripts/bundle-macos.sh
#
# Builds a release for macOS, for the architecture of the machine it runs on:
#
#   dist/OmegaCat-<version>-macos-<arch>.dmg          the app, the CLI tools, the licences
#   dist/omegacat-cli-<version>-darwin-<arch>.tar.gz  capture and ocvault alone
#
#   ./scripts/bundle-macos.sh
#   ./scripts/bundle-macos.sh --qt ~/Qt/6.8.3/macos --version v0.3.0
#
# Options:
#   --qt <prefix>      the Qt to build and deploy with (default: as build-app.sh finds it)
#   --version <v>      stamp this version (default: git describe --tags)
#   --no-dmg           stop after staging: build/bundle-macos/stage
#
# Needs what build-app.sh needs, plus macdeployqt from the SAME Qt (it is taken
# from that prefix, never from PATH), codesign and hdiutil, which come with
# the Xcode command-line tools.
#
# SIGNING. The app is signed ad hoc, which Apple Silicon requires of every
# binary macdeployqt has rewritten -- unsigned, it is killed at launch with no
# message. Ad hoc is not a Developer ID: a copy downloaded from GitHub is
# quarantined and Gatekeeper refuses to open it ("damaged" or "cannot be
# verified"). The DMG carries READ ME FIRST.txt with the one-line fix. Real
# signing and notarization need an Apple Developer account; pass the identity
# as OMEGACAT_CODESIGN_IDENTITY and it is used instead of ad hoc, and the
# notarization itself is left to xcrun notarytool.
#
# ONE ARCHITECTURE. The Go archive is built for this machine's CPU, so an
# arm64 Mac produces an arm64 app. An Intel build comes from an Intel Mac, or
# from `arch -x86_64` with an x86_64 Go and a Qt that has x86_64 slices.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

QT_ARG=()
VERSION=""
VERSION_SET=0
MAKE_DMG=1
BUILD_DIR="build/bundle-macos"

usage() { sed -n '3,33p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }
die() { echo "error: $*" >&2; exit 1; }
say() { echo "==> $*"; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --qt) [[ $# -ge 2 ]] || die "--qt needs a path"; QT_ARG=(--qt "$2"); shift 2 ;;
        --version) [[ $# -ge 2 ]] || die "--version needs a value"; VERSION="$2"; VERSION_SET=1; shift 2 ;;
        --no-dmg) MAKE_DMG=0; shift ;;
        -h|--help) usage 0 ;;
        *) echo "unknown option: $1" >&2; usage 2 ;;
    esac
done

[[ "$(uname -s)" == "Darwin" ]] || die "this builds the macOS release; see bundle-linux.sh and bundle-windows.bat"
for t in codesign hdiutil otool ditto; do
    command -v "$t" >/dev/null || die "$t is not on PATH (xcode-select --install)"
done

GOARCH="$(go env GOARCH)"
[[ "${VERSION_SET}" -eq 1 ]] || VERSION="$(git describe --tags --always --dirty 2>/dev/null || true)"
NAME_VERSION="${VERSION:-dev}"

# --- the application -----------------------------------------------------------

say "building the application (${BUILD_DIR})"
./scripts/build-app.sh "${QT_ARG[@]+"${QT_ARG[@]}"}" --build-dir "${BUILD_DIR}" \
    --version "${VERSION}" --no-tests
QT_PREFIX="$(cat "${BUILD_DIR}/qt-prefix.txt")"
BUILT_APP="${BUILD_DIR}/app/omegacat.app"
[[ -d "${BUILT_APP}" ]] || die "no ${BUILT_APP} after the build"

MACDEPLOYQT=""
for m in "${QT_PREFIX}/bin/macdeployqt" "${QT_PREFIX}/libexec/macdeployqt"; do
    [[ -x "$m" ]] && { MACDEPLOYQT="$m"; break; }
done
[[ -n "${MACDEPLOYQT}" ]] || die "no macdeployqt under ${QT_PREFIX}; it must come from the Qt the app was built against"

# --- stage ---------------------------------------------------------------------

STAGE="${BUILD_DIR}/stage"
rm -rf "${STAGE}"
mkdir -p "${STAGE}/cli"
APP="${STAGE}/OmegaCat.app"
# ditto, not cp -R: it keeps the symlinks inside frameworks as symlinks, and a
# framework whose Versions/Current became a copy fails signature checks.
ditto "${BUILT_APP}" "${APP}"

say "deploying Qt with ${MACDEPLOYQT}"
# -libpath: Homebrew's Qt reaches its frameworks through rpaths macdeployqt
# does not always follow on its own.
"${MACDEPLOYQT}" "${APP}" -libpath="${QT_PREFIX}/lib" -verbose=1 > "${BUILD_DIR}/macdeployqt.log" 2>&1 \
    || { tail -30 "${BUILD_DIR}/macdeployqt.log" >&2; die "macdeployqt failed; log: ${BUILD_DIR}/macdeployqt.log"; }

# macdeployqt's ERROR lines are not the verdict. It copies EVERY plugin in the
# Qt plugin directory, and Homebrew's is shared by every Qt module installed:
# qtpdf's image plugin and qtvirtualkeyboard's input plugin arrive needing
# frameworks this app never links, and are reported as errors while the
# deployment of the app itself is fine. It also verifies its own interim
# signature after rewriting Homebrew's dylibs, which fails and does not
# matter: the bundle is signed again below, after pruning. The verdict is
# macho-check.sh, which resolves every dependency the way dyld will.
errs="$(grep -ci '^ERROR' "${BUILD_DIR}/macdeployqt.log" || true)"
[[ "${errs}" -gt 0 ]] && say "macdeployqt reported ${errs} error line(s); checked below rather than trusted (log: ${BUILD_DIR}/macdeployqt.log)"

# Only the plugin types OmegaCat uses. A Widgets application with SVG icons
# needs a platform, the image formats (SVG among them) and the icon engines;
# styles is kept for the native style Qt may probe. Everything else a shared
# plugin directory contributed -- input methods, SQL drivers, TLS backends,
# permissions -- is weight at best and an unloadable plugin at worst.
KEEP_PLUGINS=" platforms imageformats iconengines styles "
if [[ -d "${APP}/Contents/PlugIns" ]]; then
    for d in "${APP}/Contents/PlugIns"/*; do
        [[ -d "$d" ]] || continue
        if [[ "${KEEP_PLUGINS}" != *" $(basename "$d") "* ]]; then
            say "dropping plugins/$(basename "$d") (not used by OmegaCat)"
            rm -rf "$d"
        fi
    done
fi

# Then every remaining file must load. A plugin that cannot is removed (Qt
# would skip it anyway); anything else that cannot is a broken bundle.
say "checking every dependency in the bundle"
"${ROOT}/scripts/macho-check.sh" --prune-plugins "${APP}" \
    || die "OmegaCat.app would not load on another Mac (above); log: ${BUILD_DIR}/macdeployqt.log"

# Licences inside the app as well as beside it -- before signing, which a later
# change to the bundle would invalidate.
[[ -f LICENSE ]] || die "LICENSE is missing; GPLv3 requires it to accompany the binary"
[[ -f licenses/THIRD_PARTY_NOTICES.md ]] || die "licenses/THIRD_PARTY_NOTICES.md is missing (python3 scripts/gen-third-party-notices.py)"
if command -v python3 >/dev/null; then
    python3 scripts/gen-third-party-notices.py --check || die "the notices are stale; a release must carry current ones"
fi
stage_licences() { cp LICENSE "$1/"; rm -rf "$1/licenses"; cp -R licenses "$1/licenses"; }
stage_licences "${APP}/Contents/Resources"
stage_licences "${STAGE}"

IDENTITY="${OMEGACAT_CODESIGN_IDENTITY:--}"
say "signing $([[ "${IDENTITY}" == "-" ]] && echo "ad hoc" || echo "as ${IDENTITY}")"
SIGN_ARGS=(--force --deep --sign "${IDENTITY}")
[[ "${IDENTITY}" != "-" ]] && SIGN_ARGS+=(--options runtime --timestamp)
codesign "${SIGN_ARGS[@]}" "${APP}" > "${BUILD_DIR}/codesign.log" 2>&1 \
    || { cat "${BUILD_DIR}/codesign.log" >&2; die "codesign failed"; }

# --- the command-line tools ----------------------------------------------------

LDFLAGS="-s -w"
[[ -n "${VERSION}" ]] && LDFLAGS="${LDFLAGS} -X github.com/scottpeterman/omegacatqt/internal/buildinfo.Version=${VERSION}"
for d in cmd/*/; do
    ls "${d}"*.go >/dev/null 2>&1 || continue
    t="$(basename "${d}")"
    say "building ${t}"
    CGO_ENABLED=0 GOOS=darwin GOARCH="${GOARCH}" go build -trimpath -ldflags "${LDFLAGS}" -o "${STAGE}/cli/${t}" "./cmd/${t}"
done

cat > "${STAGE}/READ ME FIRST.txt" <<README
OmegaCat ${NAME_VERSION} for macOS (${GOARCH})

Install: drag OmegaCat.app to Applications.

FIRST LAUNCH. This build is not notarized by Apple, so macOS blocks a copy
downloaded from the internet and may call it damaged. It is not. Clear the
download quarantine once, in Terminal:

    xattr -dr com.apple.quarantine /Applications/OmegaCat.app

or right-click OmegaCat.app, choose Open, and confirm.

COMMAND-LINE TOOLS. cli/capture and cli/ocvault run from anywhere; copy them
onto your PATH, e.g.:

    sudo cp cli/capture cli/ocvault /usr/local/bin/
    xattr -d com.apple.quarantine /usr/local/bin/capture /usr/local/bin/ocvault

LICENCE. GPL-3.0; see LICENSE and licenses/ (also inside the app, and in
OmegaCat > About OmegaCat > Attributions).
README

# --- verify ----------------------------------------------------------------------

need() { [[ -e "${APP}/$1" ]] || die "$1 is missing from OmegaCat.app: $2"; }
need Contents/MacOS/omegacat "the application itself"
need Contents/Resources/omegacat.icns "the Dock and Finder icon"
for fw in QtCore QtGui QtWidgets QtSvg; do
    need "Contents/Frameworks/${fw}.framework" "macdeployqt did not copy it"
done
need Contents/PlugIns/platforms/libqcocoa.dylib "without it the app exits at launch"
# The theme's checkboxes and dropdown arrows are SVG, drawn through this
# plugin. Missing, nothing fails: every checkbox is an empty square.
need Contents/PlugIns/imageformats/libqsvg.dylib "every checkbox and dropdown arrow would draw blank"
need Contents/Resources/licenses/THIRD_PARTY_NOTICES.md "the notices"

# Nothing pointing at the build machine's libraries is left: macho-check.sh
# above fails on any dependency outside the bundle, which is the check this
# used to do by grepping for the Qt prefix -- and also catches a Homebrew
# library that is not Qt's.

codesign --verify --deep --strict "${APP}" 2>"${BUILD_DIR}/verify.log" \
    || { cat "${BUILD_DIR}/verify.log" >&2; die "the signature does not verify; the app would be killed at launch"; }

got="$("${APP}/Contents/MacOS/omegacat" --version 2>/dev/null | tail -1 || true)"
if [[ -n "${VERSION}" && "${got}" != "omegacat ${VERSION}" ]]; then
    die "the bundle reports \"${got}\", expected \"omegacat ${VERSION}\""
fi
say "starts: ${got}"

if [[ -n "${VERSION}" ]]; then
    for t in "${STAGE}"/cli/*; do
        "$t" -version 2>/dev/null | grep -qF "${VERSION}" || die "$(basename "$t") -version does not report ${VERSION}"
    done
fi

# --- package -----------------------------------------------------------------------

mkdir -p dist
CLI_DIR="${BUILD_DIR}/omegacat-cli-${NAME_VERSION}-darwin-${GOARCH}"
rm -rf "${CLI_DIR}"
mkdir -p "${CLI_DIR}"
cp "${STAGE}/cli/"* "${CLI_DIR}/"
stage_licences "${CLI_DIR}"
CLI_TAR="dist/omegacat-cli-${NAME_VERSION}-darwin-${GOARCH}.tar.gz"
tar -C "${BUILD_DIR}" -czf "${CLI_TAR}" "$(basename "${CLI_DIR}")"
say "wrote ${CLI_TAR}"

if [[ "${MAKE_DMG}" -eq 1 ]]; then
    ln -s /Applications "${STAGE}/Applications"
    DMG="dist/OmegaCat-${NAME_VERSION}-macos-${GOARCH}.dmg"
    rm -f "${DMG}"
    say "writing ${DMG}"
    hdiutil create -volname "OmegaCat ${NAME_VERSION}" -srcfolder "${STAGE}" -ov -format UDZO "${DMG}" >/dev/null \
        || die "hdiutil could not create the DMG"
    say "wrote ${DMG} ($(du -h "${DMG}" | cut -f1))"
else
    say "staged ${STAGE}"
fi