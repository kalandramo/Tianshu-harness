#!/usr/bin/env bash
# linux-container-build.sh — **在 Linux 容器内执行**的构建链（宿主勿直接跑）。
# 由 scripts/build-linux-container.sh 注入容器后调用；按 CI workflow 同款步骤
# 出 AppImage（含 repack-appimage-pango，issue #80）。
set -euo pipefail
cd /work

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y libwebkit2gtk-4.1-dev build-essential curl wget file \
  libxdo-dev libssl-dev libayatana-appindicator3-dev librsvg2-dev \
  xdg-utils

# Node 24（与 fetch-node-runtime 的 v24.18.0 对齐，容器内 PATH 独立不碰宿主）
UNAME_M="$(uname -m)"
NODE_ARCH="$([ "$UNAME_M" = x86_64 ] && echo x64 || echo arm64)"
curl -fsSL "https://nodejs.org/dist/v24.18.0/node-v24.18.0-linux-${NODE_ARCH}.tar.xz" | tar -xJ -C /opt
export PATH="/opt/node-v24.18.0-linux-${NODE_ARCH}/bin:$PATH"
node -v

# Rust stable
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
  | sh -s -- -y --default-toolchain stable --profile default
export PATH="$HOME/.cargo/bin:$PATH"
rustc --version

# 完整性签名私钥经 -e 注入；缺钥直接失败（与 mac/windows 打包同一纪律）。
if [ -z "${RIVET_RELEASE_KEY_PKCS8:-}" ]; then
  echo "✗ 缺 RIVET_RELEASE_KEY_PKCS8——导出后重跑（宿主 ~/.tianshu/release.key）" >&2
  exit 1
fi

npm ci
npm run build
cd desktop
npm ci
node scripts/fetch-node-runtime.js
# tauri:build:appimage = tauri build --bundles appimage + repack-appimage-pango.sh
npm run tauri:build:appimage

echo "=== AppImage 产物 ==="
ls -lh src-tauri/target/release/bundle/appimage/*.AppImage
