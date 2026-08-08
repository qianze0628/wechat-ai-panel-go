# ============================================
#  WeChat AI Panel (Go) - Windows 启动脚本
#  自动定位可执行文件并启动
# ============================================
$ErrorActionPreference = 'SilentlyContinue'

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $ScriptDir

Write-Host '=============================================='
Write-Host '  WeChat AI Panel (Go) Launcher'
Write-Host '=============================================='

# 查找可执行文件: 本目录 / bin\ / release\
$Bin = $null
foreach ($cand in @(
  (Join-Path $ScriptDir 'wechat-ai-panel.exe'),
  (Join-Path $ScriptDir 'bin\wechat-ai-panel.exe'),
  (Join-Path $ScriptDir 'release\wechat-ai-panel-windows-amd64.exe')
)) {
  if (Test-Path $cand) { $Bin = $cand; break }
}

if (-not $Bin) {
  Write-Host '[ERROR] 未找到面板可执行文件 (wechat-ai-panel.exe)。请先编译: go build -o bin\wechat-ai-panel.exe ./cmd/server' -ForegroundColor Red
  exit 1
}
Write-Host "[0/2] 可执行文件: $Bin"

# 确保 config.local.json 存在 (从模板复制)
if (-not (Test-Path (Join-Path $ScriptDir 'config.local.json')) -and (Test-Path (Join-Path $ScriptDir 'config.local.example.json'))) {
  Write-Host '[1/2] 未找到 config.local.json, 从模板复制 (请按需修改)'
  Copy-Item (Join-Path $ScriptDir 'config.local.example.json') (Join-Path $ScriptDir 'config.local.json')
}

Write-Host '[2/2] 启动面板...'
& $Bin
