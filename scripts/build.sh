#!/usr/bin/env bash
# scripts/build.sh
#
# Full build for Linux and macOS.
#
#   ./scripts/build.sh                   vet, test, command-line tools, then the
#                                        C surface and its probe (CMake)
#   ./scripts/build.sh --no-test         build only
#   ./scripts/build.sh --no-capi         tools only; no C compiler needed
#   ./scripts/build.sh --cross           also the tools for every release platform
#   ./scripts/build.sh --version v0.2.0  stamp this (default: git describe)
#
# Output:
#   build/bin/<tool>                     tools for this machine (CGO_ENABLED=0)
#   build/cross/<os>-<arch>/<tool>       with --cross
#   build/cmake/                         the c-archive, capi_probe, fakedevice
#
# Tools are built with CGO_ENABLED=0, so --cross makes Windows and Linux
# binaries on a Mac with no other toolchain. The C surface needs cgo and is
# built per OS, never cross-compiled.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

RUN_TESTS=1
BUILD_CAPI=1
CROSS=0
VERSION=""
RELEASE_PLATFORMS="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64"
PKG_BUILDINFO="github.com/scottpeterman/omegacatqt/internal/buildinfo"

usage() {
    sed -n '3,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-test) RUN_TESTS=0; shift ;;
        --no-capi) BUILD_CAPI=0; shift ;;
        --cross) CROSS=1; shift ;;
        --version)
            [[ $# -ge 2 ]] || { echo "error: --version needs a value" >&2; exit 2; }
            VERSION="$2"; shift 2 ;;
        --version=*) VERSION="${1#*=}"; shift ;;
        -h|--help) usage 0 ;;
        *) echo "unknown option: $1" >&2; usage 2 ;;
    esac
done

die() { echo "error: $*" >&2; exit 1; }

# --- preflight -------------------------------------------------------------

command -v go >/dev/null || die "go is not on PATH"
[[ -f go.mod ]] || die "no go.mod at ${ROOT}"

# A go.work above the repo silently replaces the module set, and every ./...
# then misses -- which reads as a broken tree rather than a stray workspace.
WORKFILE="$(go env GOWORK 2>/dev/null || true)"
if [[ -n "${WORKFILE}" ]] && ! go list ./internal/capture >/dev/null 2>&1; then
    die "a Go workspace is shadowing this module: ${WORKFILE}
  Build with GOWORK=off:  GOWORK=off ./scripts/build.sh"
fi

# A go.sum missing entries -- no file at all, or one shipped without the
# checksums for modules go.mod pins -- stops vet with "missing go.sum entry".
# tidy fills them in and never upgrades a module go.mod already requires, so
# running it here is safe. Not under GOFLAGS=-mod=mod (a Claude sandbox), where
# tidy cannot reach the module proxy and the build writes go.sum itself.
if [[ "$(go env GOFLAGS)" != *-mod=mod* ]]; then
    if [[ ! -f go.sum ]] || go list -deps ./... 2>&1 >/dev/null | grep -q "missing go.sum entry"; then
        echo "==> go mod tidy (go.sum is missing entries)"
        go mod tidy
    fi
fi

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

[[ -n "${VERSION}" ]] || VERSION="$(git describe --tags --always --dirty 2>/dev/null || true)"
LDFLAGS="-s -w"
[[ -n "${VERSION}" ]] && LDFLAGS="${LDFLAGS} -X ${PKG_BUILDINFO}.Version=${VERSION}"

printf '==> toolchain: %s, version %s\n' "$(go version | awk '{print $3}')" "${VERSION:-(unstamped)}"

# --- vet and test ----------------------------------------------------------

if [[ "${RUN_TESTS}" -eq 1 ]]; then
    echo "==> go vet"
    go vet ./...

    # The notices are compiled into the application, so a stale file ships
    # inside every binary. app_probe catches a version drift; this catches
    # everything else the generator writes (a licence text that changed, the
    # template database's provenance counts).
    if command -v python3 >/dev/null; then
        echo "==> third-party notices"
        python3 scripts/gen-third-party-notices.py --check
    else
        echo "==> third-party notices: python3 not found, not checked"
    fi

    # -race needs cgo and a C compiler; without one the tests still run.
    CC_BIN="$(go env CC 2>/dev/null || true)"
    if [[ "$(go env CGO_ENABLED)" == "1" ]] && command -v "${CC_BIN:-cc}" >/dev/null 2>&1; then
        echo "==> go test (race)"
        go test -race -count=1 ./...
        # The template engine's corpus parity tests skip under -race (they
        # are minutes there and exercise no concurrency); run them plainly.
        echo "==> go test (tfsmfire corpus parity)"
        go test -count=1 ./internal/tfsmfire
    else
        echo "==> go test (no C compiler for -race; running without it)"
        go test -count=1 ./...
    fi
fi

# --- tools -----------------------------------------------------------------
# Every cmd/ directory with Go source is a tool, so a new one needs no edit.

TOOLS=""
for d in cmd/*/; do
    ls "${d}"*.go >/dev/null 2>&1 || continue
    t="${d#cmd/}"
    TOOLS="${TOOLS} ${t%/}"
done

build_one() { # goos goarch outdir tool
    local ext=""
    [[ "$1" == "windows" ]] && ext=".exe"
    CGO_ENABLED=0 GOOS="$1" GOARCH="$2" \
        go build -trimpath -ldflags "${LDFLAGS}" -o "$3/$4${ext}" "./cmd/$4"
}

echo "==> tools ($(go env GOOS)/$(go env GOARCH))"
mkdir -p build/bin
for t in ${TOOLS}; do
    build_one "$(go env GOOS)" "$(go env GOARCH)" build/bin "${t}"
done

if [[ "${CROSS}" -eq 1 ]]; then
    for plat in ${RELEASE_PLATFORMS}; do
        goos="${plat%/*}"; goarch="${plat#*/}"
        echo "==> tools (${plat})"
        mkdir -p "build/cross/${goos}-${goarch}"
        for t in ${TOOLS}; do
            build_one "${goos}" "${goarch}" "build/cross/${goos}-${goarch}" "${t}"
        done
    done
fi

# --- C surface -------------------------------------------------------------

if [[ "${BUILD_CAPI}" -eq 1 ]]; then
    command -v cmake >/dev/null || die "cmake is not on PATH (or pass --no-capi)"
    echo "==> C surface (cmake)"
    if stale_build_dir build/cmake; then
        echo "==> build/cmake was configured for another checkout; starting it fresh"
        rm -rf build/cmake
    fi
    cmake -S . -B build/cmake -DCMAKE_BUILD_TYPE=Release -DGO_EXECUTABLE="$(command -v go)" >/dev/null
    cmake --build build/cmake --parallel
    if [[ "${RUN_TESTS}" -eq 1 ]]; then
        echo "==> capi_probe"
        ctest --test-dir build/cmake --output-on-failure
    fi
fi

# --- report ----------------------------------------------------------------

ARTIFACT_DIRS="build/bin"
[[ -d build/cross ]] && ARTIFACT_DIRS="${ARTIFACT_DIRS} build/cross"

echo
echo "artifacts:"
find ${ARTIFACT_DIRS} -type f -perm -u+x | sort | while read -r f; do
    printf '  %-44s %6s KB\n' "${f}" "$(( $(wc -c < "${f}") / 1024 ))"
done
for f in build/cmake/libomegacat.a build/cmake/tests/capi_probe; do
    [[ -f "${f}" ]] && printf '  %-44s %6s KB\n' "${f}" "$(( $(wc -c < "${f}") / 1024 ))"
done
echo
./build/bin/ocvault -version
