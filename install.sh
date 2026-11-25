#!/bin/sh
set -eu

REPO="Anurag-Raut/tori"
PREFIX="${PREFIX:-$HOME/.local/bin}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "tori installer: missing required command: $1" >&2
    exit 1
  fi
}

need_cmd uname
need_cmd tar
need_cmd mktemp

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
  fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
  fetch_stdout() { wget -qO- "$1"; }
else
  echo "tori installer: curl or wget is required" >&2
  exit 1
fi

OS_RAW="$(uname -s)"
case "$OS_RAW" in
  Linux) OS="linux" ;;
  Darwin) OS="darwin" ;;
  *)
    echo "tori installer: unsupported OS '$OS_RAW' (supported: Linux, Darwin)" >&2
    exit 1
    ;;
esac

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    echo "tori installer: unsupported architecture '$ARCH_RAW' (supported: amd64, arm64)" >&2
    exit 1
    ;;
esac

TAG="${VERSION:-}"
if [ -z "$TAG" ]; then
  TAG="$(fetch_stdout "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
fi
if [ -z "$TAG" ]; then
  echo "tori installer: could not determine latest release version" >&2
  exit 1
fi

VER="${TAG#v}"
ARCHIVE="tori_${VER}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

echo "Downloading tori ${TAG} (${OS}/${ARCH})..."
fetch "${BASE_URL}/${ARCHIVE}" "${TMPDIR}/${ARCHIVE}"

if fetch "${BASE_URL}/checksums.txt" "${TMPDIR}/checksums.txt" 2>/dev/null; then
  EXPECTED="$(grep " ${ARCHIVE}\$" "${TMPDIR}/checksums.txt" | awk '{print $1}' | head -n 1 || true)"
  if [ -n "$EXPECTED" ]; then
    ACTUAL=""
    if command -v sha256sum >/dev/null 2>&1; then
      ACTUAL="$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
      ACTUAL="$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
    fi
    if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
      echo "tori installer: checksum mismatch for ${ARCHIVE}" >&2
      exit 1
    fi
  fi
fi

tar -xzf "${TMPDIR}/${ARCHIVE}" -C "${TMPDIR}"
mkdir -p "$PREFIX"
install -m 0755 "${TMPDIR}/tori" "${PREFIX}/tori"

echo "Installed tori to ${PREFIX}/tori"
case ":${PATH}:" in
  *":${PREFIX}:"*) ;;
  *) echo "Add ${PREFIX} to your PATH to run 'tori' directly." ;;
esac
