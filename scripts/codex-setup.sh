#!/usr/bin/env bash
# Run as the setup script of a Codex cloud Ubuntu environment.
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]] || ! command -v apt-get >/dev/null 2>&1; then
  echo "This setup script requires an Ubuntu/Debian Linux environment." >&2
  exit 1
fi

for tool in node npm rustup; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Missing $tool: select the Codex universal image with Node.js and Rust." >&2
    exit 1
  fi
done

# Vite 7 requires Node.js 20.19+ or 22.12+.
node -e 'const [major, minor] = process.versions.node.split(".").map(Number); if (!((major === 20 && minor >= 19) || (major === 22 && minor >= 12) || major > 22)) { console.error("Select a supported Node.js LTS version in the environment settings."); process.exit(1); }'

cd "$(dirname "${BASH_SOURCE[0]}")/.."

apt_command=(apt-get)
if [[ "$EUID" -ne 0 ]]; then
  apt_command=(sudo apt-get)
fi

# Keep in sync with the Linux check job in .github/workflows/ci.yml.
"${apt_command[@]}" update
"${apt_command[@]}" install -y \
  libwebkit2gtk-4.1-dev \
  libayatana-appindicator3-dev \
  librsvg2-dev \
  libxdo-dev \
  libssl-dev \
  libasound2-dev \
  build-essential \
  pkg-config \
  file

rustup toolchain install stable --profile minimal --component rustfmt --component clippy
rustup override set stable

if ! command -v pnpm >/dev/null 2>&1 || [[ "$(pnpm --version)" != 9.* ]]; then
  npm install --global pnpm@9
fi

pnpm install --frozen-lockfile
cargo fetch --locked

echo "Codex development dependencies are ready. No Apple live tests were run."
