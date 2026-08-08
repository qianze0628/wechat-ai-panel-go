#!/usr/bin/env bash
# ============================================
#  WeChat AI Panel (Go) - Linux/macOS 启动脚本
#  自动定位配置文件所在目录并启动
# ============================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo '=============================================='
echo '  WeChat AI Panel (Go) Launcher'
echo '=============================================='

# 查找可执行文件: 本目录 / bin/ / release/
BIN=""
for cand in "$SCRIPT_DIR/wechat-ai-panel" "$SCRIPT_DIR/bin/wechat-ai-panel" "$SCRIPT_DIR/release/wechat-ai-panel-linux-amd64"; do
  if [ -x "$cand" ]; then
    BIN="$cand"
    break
  fi
done

if [ -z "$BIN" ]; then
  echo '[ERROR] 未找到面板可执行文件 (wechat-ai-panel)。请先编译: go build -o bin/wechat-ai-panel ./cmd/server' >&2
  exit 1
fi

echo "[0/2] 可执行文件: $BIN"

# 确保 config.local.json 存在 (从模板复制)
if [ ! -f "$SCRIPT_DIR/config.local.json" ] && [ -f "$SCRIPT_DIR/config.local.example.json" ]; then
  echo '[1/2] 未找到 config.local.json, 从模板复制 (请按需修改)'
  cp "$SCRIPT_DIR/config.local.example.json" "$SCRIPT_DIR/config.local.json"
fi

echo '[2/2] 启动面板...'
mkdir -p logs
exec "$BIN"
