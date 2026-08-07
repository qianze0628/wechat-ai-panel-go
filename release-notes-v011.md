## 🔧 v0.1.1 — 修复与体验优化

### 🐛 修复

- **配置目录向上查找**: 修复 exe 放在 `bin/` 子目录时(如双击运行),无法加载项目根目录 `config.local.json` 的问题。
  此前会导致 `AstrBot 启动失败: chdir .../.astrbot-root 不存在`、`wechat-bot 不存在`、`qr-server.js 不存在`。
  现在 exe 在任何位置都能自动向上找到配置文件。

### ✨ 改进

- **启动按钮体验**: 点击"启动全部服务"或单个服务启动后, 自动等待健康检查通过, 弹出成功提示弹窗(含各服务状态), 不再需要二次点击刷新状态。
- **README 主版本定位**: 移除"重构"字样, 明确 Go 版为官方主版本, 突出单文件可执行、性能与跨平台优势。

### 📦 平台支持 (与 v0.1.0 相同)

| 平台 | 架构 | 文件 |
|---|---|---|
| Windows | amd64 | `wechat-ai-panel-windows-amd64.exe.zip` |
| Linux | amd64 | `wechat-ai-panel-linux-amd64.tar.gz` |
| Linux | arm64 | `wechat-ai-panel-linux-arm64.tar.gz` |
| macOS | amd64 (Intel) | `wechat-ai-panel-darwin-amd64.tar.gz` |
| macOS | arm64 (M系列) | `wechat-ai-panel-darwin-arm64.tar.gz` |

每个压缩包内含: 可执行文件 + `config.local.example.json` 配置模板。

### 🚀 快速开始

```bash
# 1. 解压对应平台压缩包
# 2. 复制配置模板并修改 (路径/端口)
cp config.local.example.json config.local.json
# 3. 运行 (exe 放任意目录均可, 自动向上找配置)
./wechat-ai-panel
# 4. 打开面板 http://localhost:8081
```

### 🐳 Docker

```bash
git clone https://github.com/qianze0628/wechat-ai-panel-go
cd wechat-ai-panel-go
docker compose up -d --build
```

> 完整文档: [README](https://github.com/qianze0628/wechat-ai-panel-go/blob/master/README.md)
