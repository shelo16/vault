#!/bin/sh
# Installs or updates vault on Linux and macOS:
#   curl -fsSL https://github.com/shelo16/vault/releases/latest/download/install.sh | sh
# Options (env vars): VAULT_REPO=owner/repo  VAULT_INSTALL_DIR=/some/bin
set -eu

REPO="${VAULT_REPO:-shelo16/vault}"

case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=macos ;;
  *) echo "vault: unsupported system $(uname -s) — on Windows use install.ps1" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "vault: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${VAULT_INSTALL_DIR:-}" ]; then
  dir="$VAULT_INSTALL_DIR"
elif [ "$os" = macos ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi

url="https://github.com/$REPO/releases/latest/download/vault-$os-$arch"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
echo "downloading vault-$os-$arch ..."
curl -fL --progress-bar -o "$tmp" "$url"
chmod 755 "$tmp"

sudo=""
mkdir -p "$dir" 2>/dev/null || true
if [ ! -d "$dir" ] || [ ! -w "$dir" ]; then
  echo "$dir needs admin rights — you may be asked for your login password"
  sudo=sudo
  $sudo mkdir -p "$dir"
fi
# copy next to the target, then rename: safe even while vault is running
$sudo cp "$tmp" "$dir/vault.new"
$sudo chmod 755 "$dir/vault.new"
$sudo mv -f "$dir/vault.new" "$dir/vault"

echo "installed: $("$dir/vault" version)  →  $dir/vault"

case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    rc="$HOME/.bashrc"; [ "$os" = macos ] && rc="$HOME/.zshrc"
    echo
    echo "$dir is not on your PATH yet. Run this, then open a new terminal:"
    echo "  echo 'export PATH=\"$dir:\$PATH\"' >> $rc"
    ;;
esac

if [ "$os" = linux ] && ! command -v xclip >/dev/null 2>&1 && ! command -v wl-copy >/dev/null 2>&1; then
  echo
  echo "for copy-to-clipboard install one of:  sudo apt install xclip wl-clipboard"
fi
