#!/usr/bin/env bash
# One-click local install of the cee CLI. Equivalent to `make install`, for
# anyone without make. Builds and installs the `cee` command using only the
# Go toolchain, then tells you how to put it on your PATH if it isn't already.
set -euo pipefail

# Run from the repo root regardless of where the script is invoked.
cd "$(dirname "$0")"

if ! command -v go >/dev/null 2>&1; then
  echo "error: Go is not installed or not on PATH. Install Go 1.22+ and retry." >&2
  exit 1
fi

echo "==> building and installing cee"
go install -trimpath ./cmd/cee

# Mirror `go install`'s destination: GOBIN if set, else GOPATH/bin.
install_dir="$(go env GOBIN)"
[ -z "$install_dir" ] && install_dir="$(go env GOPATH)/bin"
bin="$install_dir/cee"

if [ ! -x "$bin" ]; then
  echo "error: expected the installed binary at $bin, but it is missing" >&2
  exit 1
fi

echo "==> installed: $bin"
"$bin" --help >/dev/null 2>&1 || true   # smoke check

case ":$PATH:" in
  *":$install_dir:"*)
    echo "==> $install_dir is on your PATH. Try:  cee --help"
    ;;
  *)
    echo "==> NOTE: $install_dir is not on your PATH."
    echo "    Add it (zsh) and reload your shell:"
    echo "      echo 'export PATH=\"$install_dir:\$PATH\"' >> ~/.zshrc && source ~/.zshrc"
    echo "    Or run it by full path for now:  $bin --help"
    ;;
esac
