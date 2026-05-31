#!/usr/bin/env sh
# ps3 — PersonalS3 CLI installer
#
# Usage:
#   curl -fsSL https://personals3.tech/install | sh
#
# Or pinned to a version:
#   curl -fsSL https://personals3.tech/install | sh -s -- v1.0.0
#
# Honours:
#   PS3_INSTALL_DIR  — override the install location (default: /usr/local/bin, falls back to ~/.local/bin)
#   PS3_VERSION      — install a specific tag instead of the latest

set -eu

REPO="personals3/cli"
BIN_NAME="ps3"
VERSION="${1:-${PS3_VERSION:-}}"

# ---- detect OS + arch -------------------------------------------------------
detect_platform() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    linux)  os=linux ;;
    darwin) os=macos ;;
    *)
      printf 'ps3: unsupported OS: %s\n' "$os" >&2
      printf '    For Windows, download the .zip from https://github.com/%s/releases/latest\n' "$REPO" >&2
      exit 1
      ;;
  esac

  arch=$(uname -m)
  case "$arch" in
    x86_64 | amd64)  arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *)
      printf 'ps3: unsupported architecture: %s\n' "$arch" >&2
      exit 1
      ;;
  esac

  echo "${os}_${arch}"
}

# ---- choose install dir -----------------------------------------------------
choose_install_dir() {
  if [ -n "${PS3_INSTALL_DIR:-}" ]; then
    echo "$PS3_INSTALL_DIR"
    return
  fi
  if [ -w /usr/local/bin ] 2>/dev/null; then
    echo /usr/local/bin
    return
  fi
  # Fallback: user-local. Make sure it's on $PATH.
  mkdir -p "$HOME/.local/bin"
  echo "$HOME/.local/bin"
}

# ---- resolve version --------------------------------------------------------
resolve_version() {
  if [ -n "$VERSION" ]; then
    echo "$VERSION"
    return
  fi
  # Latest release: redirected URL contains the tag.
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
        "https://github.com/${REPO}/releases/latest")
  echo "${url##*/}"
}

main() {
  command -v curl >/dev/null 2>&1 || {
    printf 'ps3: curl is required\n' >&2; exit 1;
  }
  command -v tar >/dev/null 2>&1 || {
    printf 'ps3: tar is required\n' >&2; exit 1;
  }

  platform=$(detect_platform)
  install_dir=$(choose_install_dir)
  version=$(resolve_version)

  if [ -z "$version" ] || [ "$version" = "releases" ]; then
    printf 'ps3: could not resolve latest version (no releases yet?)\n' >&2
    exit 1
  fi

  version_clean="${version#v}"
  archive="${BIN_NAME}_${version_clean}_${platform}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${version}/${archive}"

  printf 'ps3: installing %s for %s into %s\n' "$version" "$platform" "$install_dir"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  curl -fsSL -o "$tmp/$archive" "$url"

  # Best-effort checksum verification.
  if curl -fsSL -o "$tmp/checksums.txt" \
        "https://github.com/${REPO}/releases/download/${version}/checksums.txt" 2>/dev/null; then
    if command -v shasum >/dev/null 2>&1; then
      (cd "$tmp" && grep " $archive\$" checksums.txt | shasum -a 256 -c -) \
        || { printf 'ps3: checksum mismatch\n' >&2; exit 1; }
    elif command -v sha256sum >/dev/null 2>&1; then
      (cd "$tmp" && grep " $archive\$" checksums.txt | sha256sum -c -) \
        || { printf 'ps3: checksum mismatch\n' >&2; exit 1; }
    fi
  fi

  tar -xzf "$tmp/$archive" -C "$tmp"
  install -m 0755 "$tmp/$BIN_NAME" "$install_dir/$BIN_NAME" 2>/dev/null \
    || mv "$tmp/$BIN_NAME" "$install_dir/$BIN_NAME"

  printf '\n  installed: %s\n' "$install_dir/$BIN_NAME"
  printf '  version  : %s\n\n' "$version"

  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *)
      printf 'NOTE: %s is not on your $PATH. Add this to your shell rc:\n' "$install_dir"
      printf '  export PATH="%s:$PATH"\n\n' "$install_dir"
      ;;
  esac

  printf 'Next: ps3 login --server https://personals3.tech --email you@example.com\n'
}

main
