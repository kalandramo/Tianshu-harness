#!/usr/bin/env bash
# build-linux-container.sh — 本地 OrbStack/Docker 容器出 Linux AppImage（单架构）。
#
# 为什么本地容器而不是 CI：私有仓 GitHub Actions 分钟经常耗尽（v3.26 实证全员
# 3-4 秒无日志失败）；公有仓（harness）又没有闭源的 desktop/。容器 = 无账单、
# 双架构（arm64 原生 / amd64 走 OrbStack Rosetta）。
#
# 隔离策略：源码 tar 进 docker volume（排除 .git/target/node_modules/release/out），
# 容器内 npm ci 出的 linux node_modules 不污染宿主 mac 开发环境；产物 docker cp 回
# out/linux-<arch>/。完整性签名私钥经 -e 注入（不落盘、不进镜像层）。
#
# 用法:
#   export RIVET_RELEASE_KEY_PKCS8="$(cat ~/.tianshu/release.key)"
#   bash scripts/build-linux-container.sh arm64    # 原生（快）
#   bash scripts/build-linux-container.sh amd64    # Rosetta x86 模拟（慢）
set -euo pipefail
cd "$(dirname "$0")/.."

ARCH="${1:?用法: build-linux-container.sh <amd64|arm64>}"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "✗ arch 只支持 amd64 / arm64，收到: $ARCH" >&2; exit 1 ;;
esac
PLATFORM="linux/$ARCH"
VOL="tianshu-build-$ARCH"
OUT="out/linux-$ARCH"
IMG="ubuntu:22.04"

if [ -z "${RIVET_RELEASE_KEY_PKCS8:-}" ] && [ -f "$HOME/.tianshu/release.key" ]; then
  RIVET_RELEASE_KEY_PKCS8="$(cat "$HOME/.tianshu/release.key")"
fi
if [ -z "${RIVET_RELEASE_KEY_PKCS8:-}" ]; then
  echo "✗ 缺 RIVET_RELEASE_KEY_PKCS8（完整性签名必带，同 mac/windows 打包纪律）" >&2
  exit 1
fi
# updater 私钥：本地容器与宿主同一信任域（密钥本就在本机）。与
# build-macos-release.sh 同口径——把 PATH 解析成内容经 env 注入（tauri CLI
# 只读 TAURI_SIGNING_PRIVATE_KEY 本体，不读 _PATH；release 脚本的 PATH→内容
# 导出曾在这里踩空一次）。CI 场景（密钥进 GitHub Secrets）才是「本地补签」
# 流程存在的理由，别混用。
if [ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" ] && [ -f "$HOME/.tauri/tianshu.key" ]; then
  export TAURI_SIGNING_PRIVATE_KEY="$(cat "$HOME/.tauri/tianshu.key")"
  export TAURI_SIGNING_PRIVATE_KEY_PASSWORD="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"
fi
if [ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then
  echo "✗ 缺 updater 签名私钥（TAURI_SIGNING_PRIVATE_KEY 或 ~/.tauri/tianshu.key）" >&2
  exit 1
fi

echo "=== 拉镜像 $IMG ($PLATFORM) ==="
docker pull --platform "$PLATFORM" "$IMG"

docker volume rm -f "$VOL" >/dev/null 2>&1 || true
docker volume create "$VOL" >/dev/null
RUN_ARGS=(-d --platform "$PLATFORM" -v "$VOL":/work \
  -e RIVET_RELEASE_KEY_PKCS8="$RIVET_RELEASE_KEY_PKCS8" \
  -e TAURI_SIGNING_PRIVATE_KEY \
  -e TAURI_SIGNING_PRIVATE_KEY_PASSWORD)
CID="$(docker run "${RUN_ARGS[@]}" "$IMG" sleep infinity)"
cleanup() { docker rm -f "$CID" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "=== 拷贝源码 → 容器卷（排除 .git/target/node_modules/release/out）==="
tar cf - --exclude=.git \
  --exclude='src-tauri/target' --exclude='desktop/src-tauri/target' \
  --exclude='*/node_modules' --exclude=release --exclude=out . \
  | docker cp - "$CID":/work/

echo "=== 容器内构建（${ARCH}，首次含 apt/npm/cargo 全量下载，30 分钟级）==="
docker exec "$CID" bash /work/scripts/linux-container-build.sh

echo "=== 取回产物 → $OUT/ ==="
mkdir -p "$OUT"
rm -f "$OUT"/*.AppImage "$OUT"/*.AppImage.sig 2>/dev/null || true
docker cp "$CID":/work/desktop/src-tauri/target/release/bundle/appimage/. "$OUT"/
ls -lh "$OUT"/*.AppImage
echo "✅ 完成：$OUT/*.AppImage（签名在宿主机用 tauri signer 补，同 CI 后处理流程）"
