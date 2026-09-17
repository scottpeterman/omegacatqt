#!/usr/bin/env bash
# scripts/sandbox-modfile.sh
#
# Claude sandbox only. Writes a module file beside the real one that resolves
# golang.org/x and gopkg.in from their GitHub mirrors, because the sandbox
# cannot reach the Go module proxy. The working tree's go.mod and go.sum are
# never touched: the build is pointed at the copy with -modfile.
#
#   eval "$(scripts/sandbox-modfile.sh)"     # writes the file, sets the env
#   scripts/sandbox-modfile.sh --print       # the env lines only
#
# Writes $OMEGACAT_DEVMOD (default /tmp/omegacat-dev.mod) and its .sum. The
# mirror versions are read from go.mod, so a dependency bump needs no edit
# here; a module missing from the table below needs one line.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEVMOD="${OMEGACAT_DEVMOD:-/tmp/omegacat-dev.mod}"
DEVSUM="${DEVMOD%.mod}.sum"

emit_env() {
    cat <<ENV
export GOTOOLCHAIN=local GOPROXY=direct GOSUMDB=off GOFLAGS="-mod=mod -modfile=${DEVMOD}"
ENV
}
if [[ "${1:-}" == "--print" ]]; then emit_env; exit 0; fi

# module -> GitHub mirror
declare -A MIRROR=(
    [golang.org/x/crypto]=github.com/golang/crypto
    [golang.org/x/sys]=github.com/golang/sys
    [golang.org/x/term]=github.com/golang/term
    [golang.org/x/net]=github.com/golang/net
    [golang.org/x/text]=github.com/golang/text
    [golang.org/x/sync]=github.com/golang/sync
    [gopkg.in/yaml.v3]=github.com/go-yaml/yaml/v3
    [gopkg.in/check.v1]=github.com/go-check/check
)
# Versions for modules go.mod does not name but a dependency's tests pull in.
declare -A FALLBACK=(
    [golang.org/x/net]=v0.47.0
    [golang.org/x/text]=v0.31.0
    [golang.org/x/sync]=v0.18.0
    [gopkg.in/check.v1]=v0.0.0-20201130134442-10cb98267c6c
)

version_in_gomod() {
    awk -v m="$1" '$1 == m { print $2; exit } $2 == m { print $3; exit }' "${ROOT}/go.mod"
}

cp "${ROOT}/go.mod" "${DEVMOD}"
[[ -f "${ROOT}/go.sum" ]] && cp "${ROOT}/go.sum" "${DEVSUM}" || : > "${DEVSUM}"
{
    echo
    echo "// Sandbox only: module proxy unreachable, mirrors from GitHub."
    echo "replace ("
    for mod in $(printf '%s\n' "${!MIRROR[@]}" | sort); do
        v="$(version_in_gomod "${mod}")"
        [[ -n "${v}" ]] || v="${FALLBACK[${mod}]:-}"
        [[ "${mod}" == gopkg.in/yaml.v3 && -z "${v}" ]] && v=v3.0.1
        [[ -n "${v}" ]] || continue
        printf '\t%s => %s %s\n' "${mod}" "${MIRROR[${mod}]}" "${v}"
    done
    echo ")"
} >> "${DEVMOD}"
emit_env
