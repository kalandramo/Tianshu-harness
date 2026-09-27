#!/usr/bin/env bash
# finalize-linux-appimage.sh — 容器卷里已构建 AppImage 的后处理：pango 剔除
# 重打包（issue #80）→ 宿主机验证完整性（原版公钥）→ tauri signer 本地补签。
#
# 场景：tauri build 已出包但 updater 签名步中断（&& 链使 repack 未跑）——产物
# 在 docker volume tianshu-build-<arch> 里，本脚本就地补救，不必重跑 30 分钟构建。
#
# 用法: bash scripts/finalize-linux-appimage.sh <amd64|arm64>
set -euo pipefail
cd "$(dirname "$0")/.."

ARCH="${1:?用法: finalize-linux-appimage.sh <amd64|arm64>}"
case "$ARCH" in amd64|arm64) ;; *) echo "✗ arch 只支持 amd64/arm64" >&2; exit 1 ;; esac
VOL="tianshu-build-$ARCH"
OUT="out/linux-$ARCH"
IMG="ubuntu:22.04"
VER="$(node -p "require('./package.json').version")"
APPIMAGE="Tianshu_${VER}_$([ "$ARCH" = amd64 ] && echo amd64 || echo aarch64).AppImage"
BUNDLE="desktop/src-tauri/target/release/bundle/appimage/$APPIMAGE"
mkdir -p "$OUT"

echo "==> 容器内 pango 剔除重打包 + 抽出 rivet-runtime（${ARCH}）"
docker run --rm --platform "linux/$ARCH" \
  -v "$VOL":/work -v "$PWD/$OUT":/out \
  -e APPIMAGE_EXTRACT_AND_RUN=1 \
  "$IMG" bash -e -c "
    export DEBIAN_FRONTEND=noninteractive
    apt-get update >/dev/null
    apt-get install -y curl ca-certificates file >/dev/null
    bash /work/desktop/scripts/repack-appimage-pango.sh \"/work/$BUNDLE\"
    cd /tmp
    cp \"/work/$BUNDLE\" in.AppImage && chmod +x in.AppImage
    ./in.AppImage --appimage-extract >/dev/null
    cp -r squashfs-root/usr/lib/Tianshu/rivet-runtime /out/rivet-runtime-verify
    cp \"/work/$BUNDLE\" \"/out/$APPIMAGE\"
  "

echo "==> 宿主机验证完整性（与壳内嵌公钥同源）"
cat > /tmp/verify-linux-rt.ts <<'EOF'
import { verifyIntegrityManifest } from '/Users/h/work/revit/src/config/runtime-integrity.js'
import { LICENSE_PUBLIC_KEY_B64 } from '/Users/h/work/revit/src/config/license-keys.js'
const dir = process.argv[2]
const ok = verifyIntegrityManifest(dir, { mode: 'code', publicKeyB64: LICENSE_PUBLIC_KEY_B64 })
console.log('integrity verify:', ok ? 'PASS' : 'FAIL')
process.exit(ok ? 0 : 1)
EOF
npx tsx /tmp/verify-linux-rt.ts "$OUT/rivet-runtime-verify"
rm -rf "$OUT/rivet-runtime-verify" /tmp/verify-linux-rt.ts

echo "==> 宿主机 tauri signer 补签"
if [ -z "${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}" ]; then
  export TAURI_SIGNING_PRIVATE_KEY_PASSWORD=""
fi
(cd desktop && npx tauri signer sign \
  --private-key-path "$HOME/.tauri/tianshu.key" \
  --password "$TAURI_SIGNING_PRIVATE_KEY_PASSWORD" \
  "../$OUT/$APPIMAGE")

ls -lh "$OUT/$APPIMAGE" "$OUT/$APPIMAGE.sig"
echo "✅ 完成：$OUT/$APPIMAGE(+ .sig)——可传 release 并登记 manifest linux 条目"
