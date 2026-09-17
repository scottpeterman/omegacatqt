#!/usr/bin/env bash
# scripts/build-app.sh
#
# Builds the Qt application: finds Qt, configures, builds, and says which Qt
# the result actually runs against.
#
#   ./scripts/build-app.sh                         # find Qt, build into build/cmake
#   ./scripts/build-app.sh --qt ~/Qt/6.8.3/macos   # use this Qt
#   ./scripts/build-app.sh --probe                 # then run app_probe and capi_probe
#   ./scripts/build-app.sh --clean                 # drop the CMake cache first
#   ./scripts/build-app.sh --list                  # show every Qt found, build nothing
#   ./scripts/build-app.sh --version v0.3.0        # stamp this version (default: git describe)
#   ./scripts/build-app.sh --no-tests              # the application only, no probes
#
# The bundle scripts (bundle-linux.sh, bundle-macos.sh) build through this, so
# a bundle and a development build find Qt the same way. The Qt chosen is
# written to <build-dir>/qt-prefix.txt for them.
#
# Qt is looked for in this order; the first with Widgets AND Svg wins:
#
#   1. --qt <prefix>
#   2. $OMEGACAT_QT, then each entry of $CMAKE_PREFIX_PATH
#   3. ~/Qt/<version>/{gcc_64,gcc_arm64,macos} and /opt/Qt likewise, newest first
#   4. qmake6 / qtpaths6 / qmake on PATH
#   5. the distribution's Qt under /usr
#
# WHY THIS EXISTS. CMake remembers the Qt it found -- or that it found none --
# in the build directory's cache, and a later configure with a corrected
# CMAKE_PREFIX_PATH does not look again. Worse, the cache holds one entry per
# Qt module, so a directory first configured against one Qt and then pointed
# at another links a mix of both. That is the "it never finds Qt" experience,
# and why a fresh build directory always seemed to fix it. This script passes
# the Qt it chose explicitly and drops the cache when the cache disagrees.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

QT_PREFIX=""
BUILD_DIR="build/cmake"
BUILD_TYPE="Release"
CLEAN=0
LIST=0
PROBE=0
VERSION=""
VERSION_SET=0
TESTS=ON

usage() { sed -n '3,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }
die() { echo "error: $*" >&2; exit 1; }
say() { echo "==> $*"; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --qt) [[ $# -ge 2 ]] || die "--qt needs a path"; QT_PREFIX="$2"; shift 2 ;;
        --qt=*) QT_PREFIX="${1#*=}"; shift ;;
        --build-dir) [[ $# -ge 2 ]] || die "--build-dir needs a path"; BUILD_DIR="$2"; shift 2 ;;
        --debug) BUILD_TYPE="Debug"; shift ;;
        --clean) CLEAN=1; shift ;;
        --list) LIST=1; shift ;;
        --probe) PROBE=1; shift ;;
        --version) [[ $# -ge 2 ]] || die "--version needs a value"; VERSION="$2"; VERSION_SET=1; shift 2 ;;
        --version=*) VERSION="${1#*=}"; VERSION_SET=1; shift ;;
        --no-tests) TESTS=OFF; shift ;;
        -h|--help) usage 0 ;;
        *) echo "unknown option: $1" >&2; usage 2 ;;
    esac
done

[[ "${PROBE}" -eq 1 && "${TESTS}" == "OFF" ]] && die "--probe runs the probes that --no-tests leaves out"

# --- Qt discovery ------------------------------------------------------------

# The directory holding Qt6/Qt6Config.cmake under a prefix. Installer and
# aqtinstall trees use lib/cmake; Debian and Ubuntu put it under the
# multiarch directory; some distributions use lib64.
qt_cmake_dir() {
    local p="$1" d
    for d in "$p/lib/cmake" "$p"/lib/*-linux-gnu*/cmake "$p/lib64/cmake"; do
        [[ -f "$d/Qt6/Qt6Config.cmake" ]] && { echo "$d"; return 0; }
    done
    return 1
}

# Newer Qt keeps the number in Qt6ConfigVersionImpl.cmake and has
# Qt6ConfigVersion.cmake include it; older Qt has it in the latter directly.
qt_version() {
    sed -n 's/^[[:space:]]*set(PACKAGE_VERSION "\([0-9][0-9.]*\)").*/\1/p' \
        "$1/Qt6/Qt6ConfigVersionImpl.cmake" "$1/Qt6/Qt6ConfigVersion.cmake" 2>/dev/null | head -1
}

# 0 if the prefix is a usable Qt: 6.2 or newer, with Widgets and Svg.
# Otherwise prints why on stdout, for the candidate listing.
qt_check() {
    local p="$1" d v
    d="$(qt_cmake_dir "$p")" || { echo "no Qt 6 CMake files"; return 1; }
    v="$(qt_version "$d")"
    [[ -n "$v" ]] || { echo "unreadable version"; return 1; }
    if [[ "$(printf '%s\n6.2\n' "$v" | sort -V | head -1)" != "6.2" ]]; then
        echo "Qt $v is older than 6.2"; return 1
    fi
    [[ -f "$d/Qt6Widgets/Qt6WidgetsConfig.cmake" ]] || { echo "Qt $v has no Widgets"; return 1; }
    [[ -f "$d/Qt6Svg/Qt6SvgConfig.cmake" ]] || { echo "Qt $v has no Svg module"; return 1; }
    echo "Qt $v"
}

candidates() {
    local p
    [[ -n "${OMEGACAT_QT:-}" ]] && echo "${OMEGACAT_QT}"
    if [[ -n "${CMAKE_PREFIX_PATH:-}" ]]; then
        tr ':;' '\n\n' <<<"${CMAKE_PREFIX_PATH}"
    fi
    for base in "${HOME}/Qt" /opt/Qt; do
        [[ -d "$base" ]] || continue
        # Newest version first; an installer tree also holds Tools/, Docs/
        # and friends, which the version pattern skips.
        for p in $(ls -d "$base"/6.*/ 2>/dev/null | sort -V -r); do
            for kit in gcc_64 gcc_arm64 macos; do
                [[ -d "${p}${kit}" ]] && echo "${p}${kit}"
            done
        done
    done
    for tool in qmake6 qtpaths6 qmake; do
        command -v "$tool" >/dev/null 2>&1 || continue
        if [[ "$tool" == qtpaths6 ]]; then
            "$tool" --query QT_INSTALL_PREFIX 2>/dev/null || true
        else
            "$tool" -query QT_INSTALL_PREFIX 2>/dev/null || true
        fi
    done
    echo /usr
}

if [[ "${LIST}" -eq 1 ]]; then
    say "Qt installations, in the order they are tried"
    candidates | awk 'NF && !seen[$0]++' | while read -r p; do
        printf '    %-50s %s\n' "$p" "$(qt_check "$p" || true)"
    done
    exit 0
fi

if [[ -n "${QT_PREFIX}" ]]; then
    # An explicit path that is wrong is worth stopping for; say what is wrong.
    QT_PREFIX="$(cd "${QT_PREFIX}" 2>/dev/null && pwd)" || die "--qt: no such directory"
    why="$(qt_check "${QT_PREFIX}")" || die "--qt ${QT_PREFIX}: ${why}"
else
    rejected=()
    while read -r p; do
        [[ -d "$p" ]] || continue
        if why="$(qt_check "$p")"; then
            QT_PREFIX="$(cd "$p" && pwd)"
            break
        fi
        rejected+=("$p: ${why}")
    done < <(candidates | awk 'NF && !seen[$0]++')

    if [[ -z "${QT_PREFIX}" ]]; then
        echo "error: no usable Qt found (needs 6.2+ with Widgets and Svg)" >&2
        for r in "${rejected[@]+"${rejected[@]}"}"; do echo "    $r" >&2; done
        cat >&2 <<'HELP'

  Point at one:    ./scripts/build-app.sh --qt ~/Qt/6.8.3/gcc_64
  Ubuntu/Debian:   sudo apt install qt6-base-dev qt6-svg-dev
  aqtinstall:      aqt install-qt linux desktop 6.8.3 linux_gcc_64
HELP
        exit 1
    fi
fi

QT_CMAKE_DIR="$(qt_cmake_dir "${QT_PREFIX}")"
QT_VERSION="$(qt_version "${QT_CMAKE_DIR}")"
say "Qt ${QT_VERSION} at ${QT_PREFIX}"

# --- preflight ---------------------------------------------------------------

command -v go >/dev/null || die "go is not on PATH"
command -v cmake >/dev/null || die "cmake is not on PATH"
CC_BIN="$(go env CC)"
command -v "${CC_BIN}" >/dev/null || die "cgo's C compiler (${CC_BIN}) is not on PATH; the Go archive needs it"

# A go.work above the repo replaces the module set, and the archive step then
# fails with a message about module paths that reads like a broken checkout.
WORKFILE="$(go env GOWORK 2>/dev/null || true)"
if [[ -n "${WORKFILE}" && "${WORKFILE}" != "off" ]] && ! go list ./internal/capturerun >/dev/null 2>&1; then
    die "a Go workspace is shadowing this module: ${WORKFILE}
  Add this repo to it, or build with: GOWORK=off ./scripts/build-app.sh"
fi

# Same guard as build.sh: a go.sum without the checksums go.mod's pins need
# fails the c-archive step with "missing go.sum entry" deep inside the CMake
# build. tidy adds them and does not upgrade anything go.mod already requires.
# Skipped under GOFLAGS=-mod=mod (a Claude sandbox), where the proxy is
# unreachable and the modfile copy carries its own go.sum.
if [[ "$(go env GOFLAGS)" != *-mod=mod* ]]; then
    if [[ ! -f go.sum ]] || go list -deps ./capi 2>&1 >/dev/null | grep -q "missing go.sum entry"; then
        say "go mod tidy (go.sum is missing entries)"
        go mod tidy
    fi
fi

say "toolchain: $(go version | awk '{print $3}'), cmake $(cmake --version | head -1 | awk '{print $3}')"

# --- the cache ---------------------------------------------------------------
# Kept only if it was configured against the Qt chosen now. This is the same
# directory scripts/build.sh configures, so the two share one build.

# A build directory configured for another checkout -- the same tree synced
# from another machine, or moved -- is refused by CMake ("CMakeCache.txt
# directory ... is different"), and its objects and libomegacat.a were built
# for that machine. Nothing in it is reusable here, so start it fresh.
stale_build_dir() {
    local dir="$1" cache="$1/CMakeCache.txt" home
    [[ -f "${cache}" ]] || return 1
    home="$(grep -m1 '^CMAKE_HOME_DIRECTORY:INTERNAL=' "${cache}" | cut -d= -f2-)"
    [[ -n "${home}" && "${home}" != "$(pwd)" && "${home}" != "$(pwd -P)" ]]
}

if stale_build_dir "${BUILD_DIR}"; then
    say "${BUILD_DIR} was configured for another checkout; starting it fresh"
    rm -rf "${BUILD_DIR}"
fi

CACHE="${BUILD_DIR}/CMakeCache.txt"
drop_cache() {
    rm -f "${CACHE}"
    rm -rf "${BUILD_DIR}/CMakeFiles"
}
if [[ "${CLEAN}" -eq 1 && -f "${CACHE}" ]]; then
    say "dropping the CMake cache (--clean)"
    drop_cache
elif [[ -f "${CACHE}" ]]; then
    # Every Qt6*_DIR entry, not just Qt6_DIR: the cache holds one per module,
    # and passing -DQt6_DIR below overrides only that one. A Widgets or Svg
    # entry left over from another Qt is how a build links two of them.
    want="$(cd "${QT_CMAKE_DIR}" && pwd -P)"
    # When the Qt is Homebrew's, its modules live in per-formula kegs under
    # the same prefix; anything under it is this Qt.
    also=""
    if [[ "$(uname -s)" == "Darwin" ]] && command -v brew >/dev/null 2>&1; then
        bp="$(brew --prefix 2>/dev/null || true)"
        [[ -n "${bp}" && "${QT_PREFIX}" == "${bp}"* ]] && also="$(cd "${bp}" && pwd -P)"
    fi
    stale=""
    while IFS='=' read -r key value; do
        name="${key%%:*}"
        if [[ -z "${value}" || "${value}" == *NOTFOUND* ]]; then
            # Only a required module missing means the configure found no
            # Qt; an optional module Qt probes for is NOTFOUND in a good
            # cache, and treating that as stale reconfigured on every run.
            case "${name}" in
                Qt6_DIR|Qt6Core_DIR|Qt6Gui_DIR|Qt6Widgets_DIR|Qt6Svg_DIR)
                    stale="${name} recorded no Qt"; break ;;
            esac
            continue
        fi
        # Inside the chosen Qt as written, or once symlinks are resolved.
        # Homebrew needs the first: its lib/cmake/Qt6* entries are symlinks
        # into each formula's own Cellar keg, so every one resolves outside
        # lib/cmake and a resolved-only check dropped the cache on every run.
        # An installer tree reached through a symlink needs the second.
        real="$(cd "${value}" 2>/dev/null && pwd -P || echo "${value}")"
        if [[ "${value}" != "${QT_CMAKE_DIR}"/* && "${real}" != "${want}"/* &&
              ( -z "${also}" || "${real}" != "${also}"/* ) ]]; then
            stale="${name} points at ${value}"; break
        fi
    done < <(grep -E '^Qt6[A-Za-z]*_DIR:[A-Z]+=' "${CACHE}" || true)
    if [[ -n "${stale}" ]]; then
        say "dropping the CMake cache: ${stale}"
        drop_cache
    fi
fi

# --- configure and build -----------------------------------------------------

say "configuring ${BUILD_DIR} (${BUILD_TYPE})"
# The version the application reports. An explicit --version wins, even an
# empty one; otherwise git describe, and outside a git checkout nothing.
[[ "${VERSION_SET}" -eq 1 ]] || VERSION="$(git describe --tags --always --dirty 2>/dev/null || true)"
say "version ${VERSION:-(unstamped)}"

cmake -S . -B "${BUILD_DIR}" \
      -DCMAKE_BUILD_TYPE="${BUILD_TYPE}" \
      -DGO_EXECUTABLE="$(command -v go)" \
      -DCMAKE_PREFIX_PATH="${QT_PREFIX}" \
      -DQt6_DIR="${QT_CMAKE_DIR}/Qt6" \
      -DOMEGACAT_VERSION="${VERSION}" \
      -DOMEGACAT_BUILD_TESTS="${TESTS}" >/dev/null
printf '%s\n' "${QT_PREFIX}" > "${BUILD_DIR}/qt-prefix.txt"

say "building"
cmake --build "${BUILD_DIR}" -j"$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"

APP="${BUILD_DIR}/app/omegacat"
[[ "$(uname -s)" == "Darwin" ]] && APP="${BUILD_DIR}/app/omegacat.app/Contents/MacOS/omegacat"
[[ -x "${APP}" ]] || die "no ${APP} after the build"

# --- which Qt it runs against ------------------------------------------------
# Built against one Qt and loading another at run time -- an LD_LIBRARY_PATH
# left over from something else, most often -- starts fine and fails in odd
# places. Checked here because it is cheap here and confusing anywhere else.

if [[ "$(uname -s)" == "Darwin" ]]; then
    core="$(otool -L "${APP}" | awk '/QtCore/{print $1; exit}')"
else
    core="$(ldd "${APP}" | awk '/libQt6Core/{print $3; exit}')"
fi
if [[ -z "${core}" || "${core}" == "not" ]]; then
    die "${APP} does not resolve QtCore at all (ldd says: ${core:-nothing})"
fi
core_real="$(cd "$(dirname "${core}")" 2>/dev/null && pwd -P)/$(basename "${core}")"
prefix_real="$(cd "${QT_PREFIX}" && pwd -P)"
if [[ "${core_real}" != "${prefix_real}"/* && "${core}" != @rpath/* ]]; then
    echo "warning: built against ${QT_PREFIX} but loads ${core}" >&2
    [[ -n "${LD_LIBRARY_PATH:-}" ]] && echo "         LD_LIBRARY_PATH=${LD_LIBRARY_PATH}" >&2
else
    say "runs against ${core}"
fi

# --- optional probe ----------------------------------------------------------

if [[ "${PROBE}" -eq 1 ]]; then
    say "probes (capi_probe, app_probe)"
    ctest --test-dir "${BUILD_DIR}" --output-on-failure
fi

echo
echo "run it with:"
echo "      ${APP}                 # or --demo 40 to see a scripted run"
