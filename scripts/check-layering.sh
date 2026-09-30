#!/usr/bin/env bash
# Guards the layering rules that prose cannot enforce by itself.
#
# The core orchestrates publishing through the publisher port, so it will always
# pull net/http in transitively. What must never happen is the core reaching for
# an HTTP transport directly, which is what this check looks at.
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0

direct_imports() {
  go list -f '{{join .Imports "\n"}}' "$1"
}

check_absent() {
  local pkg="$1" pattern="$2" reason="$3"
  if direct_imports "$pkg" | grep -qE "$pattern"; then
    echo "FAIL: $pkg must not import $pattern directly ($reason)" >&2
    direct_imports "$pkg" | grep -E "$pattern" | sed 's/^/  /' >&2
    fail=1
  fi
}

check_absent "./internal/release" 'net/http|plumbing/transport/http' \
  "the core talks to the forge through the publisher and pusher ports"
check_absent "./internal/repository" 'net/http|plumbing/transport/http' \
  "repository resolves coordinates, it does not communicate"
check_absent "./internal/pusher" 'yuki-nemurenai/magic-releaser/internal/release' \
  "ports do not depend on the core they serve"

if go list -deps ./internal/publisher | grep -q "internal/release"; then
  echo "FAIL: internal/publisher must not depend on internal/release" >&2
  fail=1
fi

if go list -deps ./internal/pusher | grep -q "internal/release"; then
  echo "FAIL: internal/pusher must not depend on internal/release" >&2
  fail=1
fi

if go list -deps ./internal/repository | grep -qE "internal/(release|publisher|pusher)"; then
  echo "FAIL: internal/repository must not depend on any other internal package" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "layering: ok"
