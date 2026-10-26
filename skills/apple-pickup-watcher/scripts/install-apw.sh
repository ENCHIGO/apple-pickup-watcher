#!/bin/sh
# Install the apw CLI from the official GitHub Releases of ENCHIGO/apple-pickup-watcher.
#
#   sh install-apw.sh                 # latest release into ~/.local/bin
#   APW_VERSION=v0.7.0 sh install-apw.sh
#   APW_INSTALL_DIR=/usr/local/bin sh install-apw.sh
#
# The archive is checked against the .sha256 file published next to it; any
# mismatch or download failure aborts without installing anything.
set -eu

REPO="ENCHIGO/apple-pickup-watcher"
BASE="https://github.com/$REPO/releases"
DEST="${APW_INSTALL_DIR:-$HOME/.local/bin}"

fail() { echo "install-apw: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "需要 curl"
command -v tar >/dev/null 2>&1 || fail "需要 tar"

case "$(uname -s)" in
  Darwin) os=apple-darwin ;;
  Linux) os=unknown-linux-gnu ;;
  *) fail "不支持的系统 $(uname -s)；Windows 请用 install-apw.ps1" ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=aarch64 ;;
  x86_64 | amd64) arch=x86_64 ;;
  *) fail "不支持的 CPU 架构 $(uname -m)" ;;
esac
target="$arch-$os"
case "$target" in
  aarch64-apple-darwin | x86_64-apple-darwin | x86_64-unknown-linux-gnu) ;;
  *) fail "Release 里没有 $target 的预编译包（只有 macOS 和 x86_64 Linux）" ;;
esac

tag="${APW_VERSION:-}"
if [ -z "$tag" ]; then
  # Follow the releases/latest redirect instead of the REST API: no rate limit, no token.
  url=$(curl -fsSLo /dev/null -w '%{url_effective}' "$BASE/latest") || fail "无法访问 $BASE/latest"
  tag="${url##*/}"
fi
case "$tag" in v*) ;; *) tag="v$tag" ;; esac
case "$tag" in v[0-9]*.[0-9]*.[0-9]*) ;; *) fail "无法确定版本号（得到 '$tag'）" ;; esac

name="apw-cli-$tag-$target"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "下载 $name.tar.gz ..." >&2
curl -fsSL --retry 3 -o "$tmp/$name.tar.gz" "$BASE/download/$tag/$name.tar.gz" || fail "下载失败：$BASE/download/$tag/$name.tar.gz"
curl -fsSL --retry 3 -o "$tmp/$name.tar.gz.sha256" "$BASE/download/$tag/$name.tar.gz.sha256" || fail "下载校验文件失败"

expected=$(cut -d' ' -f1 "$tmp/$name.tar.gz.sha256")
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1)
else
  fail "需要 sha256sum 或 shasum 来校验下载"
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  fail "sha256 不匹配（期望 $expected，实际 $actual），已放弃安装"
fi

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
[ -f "$tmp/$name/apw" ] || fail "压缩包里没有 apw"
mkdir -p "$DEST"
cp "$tmp/$name/apw" "$DEST/apw.tmp.$$"
chmod 755 "$DEST/apw.tmp.$$"
mv -f "$DEST/apw.tmp.$$" "$DEST/apw"
if [ "$os" = apple-darwin ] && command -v xattr >/dev/null 2>&1; then
  # Unsigned binary: drop the quarantine flag so Gatekeeper does not block it.
  xattr -d com.apple.quarantine "$DEST/apw" 2>/dev/null || true
fi

"$DEST/apw" --version >&2 || fail "已安装到 $DEST/apw，但无法运行"
echo "已安装：$DEST/apw" >&2
case ":$PATH:" in
  *":$DEST:"*) ;;
  *) echo "提示：$DEST 不在 PATH 里，可直接用完整路径 $DEST/apw" >&2 ;;
esac
echo "$DEST/apw"
