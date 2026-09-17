#!/usr/bin/env bash
# scripts/bundle-linux.sh
#
# Builds a release for Linux:
#
#   dist/OmegaCat-<version>-<arch>.AppImage          the application
#   dist/omegacat-cli-<version>-linux-<arch>.tar.gz  capture and ocvault
#
#   ./scripts/bundle-linux.sh
#   ./scripts/bundle-linux.sh --qt ~/Qt/6.8.3/gcc_64 --version v0.3.0
#
# Options:
#   --qt <prefix>      the Qt to build and deploy with (default: as build-app.sh finds it)
#   --version <v>      stamp this version (default: git describe --tags)
#   --tools <dir>      where linuxdeploy and its Qt plugin are kept (default: build/tools)
#   --no-appimage      stop after the AppDir: build/bundle-linux/AppDir
#
# Needs go, cmake, a C/C++ compiler and Qt 6.2+ with Widgets and Svg -- what
# build-app.sh needs -- plus curl the first time, to fetch linuxdeploy and
# linuxdeploy-plugin-qt into --tools. Set LINUXDEPLOY and LINUXDEPLOY_PLUGIN_QT
# to use copies of your own instead; both are the "continuous" releases
# otherwise, which is the only channel either project publishes.
#
# GLIBC. An AppImage bundles Qt and everything else it links, but never glibc:
# it runs on distributions whose glibc is at least the one it was built
# against. So the machine this runs on sets the floor: built on Ubuntu 24.04 it
# needs glibc 2.38, and will not start on 22.04 (2.35). The script measures and
# reports the floor it produced; build on the oldest distribution you want to
# support.
#
# No FUSE needed here: linuxdeploy is run with APPIMAGE_EXTRACT_AND_RUN=1, and
# the result is checked by extracting it rather than mounting it.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

QT_ARG=()
VERSION=""
VERSION_SET=0
TOOLS="build/tools"
MAKE_APPIMAGE=1
BUILD_DIR="build/bundle-linux"

usage() { sed -n '3,33p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }
die() { echo "error: $*" >&2; exit 1; }
say() { echo "==> $*"; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --qt) [[ $# -ge 2 ]] || die "--qt needs a path"; QT_ARG=(--qt "$2"); shift 2 ;;
        --version) [[ $# -ge 2 ]] || die "--version needs a value"; VERSION="$2"; VERSION_SET=1; shift 2 ;;
        --tools) [[ $# -ge 2 ]] || die "--tools needs a directory"; TOOLS="$2"; shift 2 ;;
        --no-appimage) MAKE_APPIMAGE=0; shift ;;
        -h|--help) usage 0 ;;
        *) echo "unknown option: $1" >&2; usage 2 ;;
    esac
done

[[ "$(uname -s)" == "Linux" ]] || die "this builds the Linux release; see bundle-macos.sh and bundle-windows.bat"

case "$(uname -m)" in
    x86_64) ARCH=x86_64; GOARCH=amd64 ;;
    aarch64|arm64) ARCH=aarch64; GOARCH=arm64 ;;
    *) die "unsupported architecture $(uname -m)" ;;
esac

[[ "${VERSION_SET}" -eq 1 ]] || VERSION="$(git describe --tags --always --dirty 2>/dev/null || true)"
NAME_VERSION="${VERSION:-dev}"

# --- the application -----------------------------------------------------------

say "building the application (${BUILD_DIR})"
./scripts/build-app.sh "${QT_ARG[@]+"${QT_ARG[@]}"}" --build-dir "${BUILD_DIR}" \
    --version "${VERSION}" --no-tests
QT_PREFIX="$(cat "${BUILD_DIR}/qt-prefix.txt")"
APP_BIN="${BUILD_DIR}/app/omegacat"
[[ -x "${APP_BIN}" ]] || die "no ${APP_BIN} after the build"

# --- the command-line tools ----------------------------------------------------
# Pure Go, CGO_ENABLED=0: static, no Qt and no glibc floor of their own, which
# is why they ship as a tarball beside the AppImage rather than inside it -- an
# AppImage runs one program.

LDFLAGS="-s -w"
[[ -n "${VERSION}" ]] && LDFLAGS="${LDFLAGS} -X github.com/scottpeterman/omegacatqt/internal/buildinfo.Version=${VERSION}"
CLI_STAGE="${BUILD_DIR}/omegacat-cli-${NAME_VERSION}-linux-${GOARCH}"
rm -rf "${CLI_STAGE}"
mkdir -p "${CLI_STAGE}"
for d in cmd/*/; do
    ls "${d}"*.go >/dev/null 2>&1 || continue
    t="$(basename "${d}")"
    say "building ${t}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build -trimpath -ldflags "${LDFLAGS}" \
        -o "${CLI_STAGE}/${t}" "./cmd/${t}"
done

# --- licences --------------------------------------------------------------------
# GPLv3 needs LICENSE beside the program; the MIT, BSD and Apache components
# need their notices; the notices cite GPL-3.0.txt, LGPL-3.0.txt and
# Apache-2.0.txt "beside this file", so licenses/ goes whole.

[[ -f LICENSE ]] || die "LICENSE is missing; GPLv3 requires it to accompany the binary"
[[ -f licenses/THIRD_PARTY_NOTICES.md ]] || die "licenses/THIRD_PARTY_NOTICES.md is missing (python3 scripts/gen-third-party-notices.py)"
if command -v python3 >/dev/null; then
    python3 scripts/gen-third-party-notices.py --check || die "the notices are stale; a release must carry current ones"
fi
stage_licences() { # dir
    cp LICENSE "$1/"
    rm -rf "$1/licenses"
    cp -R licenses "$1/licenses"
}
stage_licences "${CLI_STAGE}"

mkdir -p dist
CLI_TAR="dist/omegacat-cli-${NAME_VERSION}-linux-${GOARCH}.tar.gz"
tar -C "${BUILD_DIR}" -czf "${CLI_TAR}" "$(basename "${CLI_STAGE}")"
say "wrote ${CLI_TAR}"

# --- the AppDir --------------------------------------------------------------------

APPDIR="${BUILD_DIR}/AppDir"
rm -rf "${APPDIR}"
mkdir -p "${APPDIR}/usr/bin" "${APPDIR}/usr/share/applications" \
         "${APPDIR}/usr/share/icons/hicolor/512x512/apps" "${APPDIR}/usr/share/doc/omegacat"
cp "${APP_BIN}" "${APPDIR}/usr/bin/omegacat"
cp app/appicon/omegacat.png "${APPDIR}/usr/share/icons/hicolor/512x512/apps/omegacat.png"
stage_licences "${APPDIR}/usr/share/doc/omegacat"

# omegacat.desktop, the name main.cpp gives setDesktopFileName so a Wayland
# compositor matches the window to this icon.
cat > "${APPDIR}/usr/share/applications/omegacat.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=OmegaCat
GenericName=Network Capture
Comment=Read-only configuration and state capture over SSH, with versioned storage and search
Exec=omegacat
Icon=omegacat
Terminal=false
Categories=Network;Utility;
DESKTOP

# --- linuxdeploy ---------------------------------------------------------------------

fetch_tool() { # var name url
    local var="$1" name="$2" url="$3" path="${!1:-}"
    if [[ -n "${path}" ]]; then
        [[ -x "${path}" ]] || die "${var}=${path} is not an executable file"
        echo "${path}"; return
    fi
    path="${TOOLS}/${name}"
    if [[ ! -x "${path}" ]]; then
        command -v curl >/dev/null || die "curl is needed to fetch ${name} (or set ${var})"
        mkdir -p "${TOOLS}"
        say "fetching ${name}" >&2
        curl -fsSL -o "${path}.part" "${url}" || die "could not download ${url}"
        chmod +x "${path}.part"
        mv "${path}.part" "${path}"
    fi
    echo "$(cd "$(dirname "${path}")" && pwd)/$(basename "${path}")"
}
LD="$(fetch_tool LINUXDEPLOY "linuxdeploy-${ARCH}.AppImage" \
    "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-${ARCH}.AppImage")"
LDQT="$(fetch_tool LINUXDEPLOY_PLUGIN_QT "linuxdeploy-plugin-qt-${ARCH}.AppImage" \
    "https://github.com/linuxdeploy/linuxdeploy-plugin-qt/releases/download/continuous/linuxdeploy-plugin-qt-${ARCH}.AppImage")"

# linuxdeploy finds plugins by name on PATH, so the Qt plugin goes in a
# directory of its own under the name it looks for.
PLUGIN_DIR="${BUILD_DIR}/ldplugins"
mkdir -p "${PLUGIN_DIR}"
ln -sf "${LDQT}" "${PLUGIN_DIR}/linuxdeploy-plugin-qt"

# The plugin deploys the Qt that qmake reports, so qmake has to be the Qt the
# application linked against -- the same rule as windeployqt on Windows.
QMAKE_BIN=""
for q in "${QT_PREFIX}/bin/qmake6" "${QT_PREFIX}/bin/qmake" "${QT_PREFIX}/lib/qt6/bin/qmake6" \
         "${QT_PREFIX}"/lib/*-linux-gnu*/qt6/bin/qmake6; do
    [[ -x "$q" ]] && { QMAKE_BIN="$q"; break; }
done
[[ -n "${QMAKE_BIN}" ]] || die "no qmake under ${QT_PREFIX}; the Qt plugin needs the one belonging to the Qt that was built against"

OUTPUT="dist/OmegaCat-${NAME_VERSION}-${ARCH}.AppImage"
rm -f "${OUTPUT}"

say "deploying Qt from ${QMAKE_BIN}"
LD_ARGS=(--appdir "${APPDIR}"
         --executable "${APPDIR}/usr/bin/omegacat"
         --desktop-file "${APPDIR}/usr/share/applications/omegacat.desktop"
         --icon-file "${APPDIR}/usr/share/icons/hicolor/512x512/apps/omegacat.png"
         --plugin qt)
[[ "${MAKE_APPIMAGE}" -eq 1 ]] && LD_ARGS+=(--output appimage)
# xcb is deployed by default and covers X11 and XWayland. offscreen as well:
# a few hundred KB, and it is what lets the bundle start with no display --
# the version check below, and anyone driving it headless.
env PATH="${PLUGIN_DIR}:${PATH}" QMAKE="${QMAKE_BIN}" APPIMAGE_EXTRACT_AND_RUN=1 \
    EXTRA_PLATFORM_PLUGINS="libqoffscreen.so" \
    ARCH="${ARCH}" LINUXDEPLOY_OUTPUT_VERSION="${NAME_VERSION}" \
    "${LD}" "${LD_ARGS[@]}" > "${BUILD_DIR}/linuxdeploy.log" 2>&1 \
    || { tail -40 "${BUILD_DIR}/linuxdeploy.log" >&2; die "linuxdeploy failed; full log: ${BUILD_DIR}/linuxdeploy.log"; }

if [[ "${MAKE_APPIMAGE}" -eq 1 ]]; then
    # linuxdeploy names its output from the desktop file and the version; the
    # release name is set here, not left to that.
    produced="$(ls -t OmegaCat*-"${ARCH}".AppImage 2>/dev/null | head -1 || true)"
    [[ -n "${produced}" ]] || die "linuxdeploy reported success and wrote no AppImage"
    mv "${produced}" "${OUTPUT}"
fi

# --- verify ----------------------------------------------------------------------------
# Checked on what will ship: the AppImage, extracted, not the AppDir it came
# from. Every gate here is a failure that looks fine on the build machine.

CHECK="${APPDIR}"
if [[ "${MAKE_APPIMAGE}" -eq 1 ]]; then
    rm -rf "${BUILD_DIR}/extracted"
    mkdir -p "${BUILD_DIR}/extracted"
    (cd "${BUILD_DIR}/extracted" && "${ROOT}/${OUTPUT}" --appimage-extract >/dev/null) \
        || die "the AppImage does not extract"
    CHECK="${BUILD_DIR}/extracted/squashfs-root"
fi

need() { [[ -e "${CHECK}/$1" ]] || die "$1 is missing from the bundle: $2"; }
need usr/bin/omegacat "the application itself"
need usr/share/doc/omegacat/LICENSE "GPLv3 requires it to accompany the binary"
for f in THIRD_PARTY_NOTICES.md GPL-3.0.txt LGPL-3.0.txt Apache-2.0.txt; do
    need "usr/share/doc/omegacat/licenses/${f}" "the notices cite it"
done
need usr/plugins/platforms/libqxcb.so "without a platform plugin the application exits at start"
need usr/plugins/platforms/libqoffscreen.so "the start check below, and headless use, need it"
# The theme draws its checkboxes and chevrons from SVG through QSvgRenderer's
# image plugin. Missing, nothing fails: every checkbox is an empty square.
need usr/plugins/imageformats/libqsvg.so "every checkbox and dropdown arrow would draw blank"

# Qt from the bundle, not the build machine. With the host's Qt on the loader
# path this passes on the build machine and fails everywhere else, so the
# library path is emptied for the check.
if ldd_out="$(env -u LD_LIBRARY_PATH LD_LIBRARY_PATH="${CHECK}/usr/lib" ldd "${CHECK}/usr/bin/omegacat" 2>&1)"; then
    missing="$(awk '/not found/{print $1}' <<<"${ldd_out}" | tr '\n' ' ')"
    [[ -z "${missing}" ]] || die "unresolved libraries in the bundle: ${missing}"
    qtcore="$(awk '/libQt6Core/{print $3}' <<<"${ldd_out}")"
    [[ "${qtcore}" == "${CHECK}"/* || "${qtcore}" == *"/usr/lib/libQt6Core"* ]] \
        || die "the bundled application loads Qt from ${qtcore}, not from the bundle"
fi

# It starts, reports the stamped version, and quits -- through AppRun, so the
# environment linuxdeploy's Qt hook sets up is the one tested.
if [[ -x "${CHECK}/AppRun" ]]; then
    got="$(QT_QPA_PLATFORM=offscreen "${CHECK}/AppRun" --version 2>/dev/null | tail -1 || true)"
    want="omegacat ${VERSION:-}"
    if [[ -n "${VERSION}" && "${got}" != "${want}" ]]; then
        die "the bundle reports \"${got}\", expected \"${want}\""
    fi
    say "starts: ${got}"
fi

# The tools report the stamped version too; each has its own -X flag.
if [[ -n "${VERSION}" ]]; then
    for t in "${CLI_STAGE}"/*; do
        [[ -f "$t" && -x "$t" ]] || continue
        "$t" -version 2>/dev/null | grep -qF "${VERSION}" || die "$(basename "$t") -version does not report ${VERSION}"
    done
fi

# The glibc floor: the newest GLIBC_ symbol version anything in the bundle
# needs. glibc itself is not bundled, so this is the oldest system it runs on.
floor="$( { objdump -T "${CHECK}/usr/bin/omegacat" 2>/dev/null
            find "${CHECK}/usr/lib" -name '*.so*' -type f -exec objdump -T {} \; 2>/dev/null; } \
          | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1 || true)"

echo
[[ "${MAKE_APPIMAGE}" -eq 1 ]] && say "wrote ${OUTPUT} ($(du -h "${OUTPUT}" | cut -f1))"
say "wrote ${CLI_TAR}"
[[ -n "${floor}" ]] && say "the AppImage needs ${floor/_/ } or newer on the target (built on $(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-this system}"))"
