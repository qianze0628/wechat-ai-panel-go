# 微信 AI 管理面板 (Go 重构版)

微信 AI 机器人一键部署管理面板的 **Go 原生重构**。目标：单 exe、低占用、跨平台、复用现有 React 前端。

> 这是从 `wechat-ai-panel` (Python FastAPI) 迁移的新项目。旧版功能规格见旧项目 `docs/REFACTOR_SPEC_BASELINE.md`。

## 状态

- ✅ 阶段 0/1：Go 骨架 + 前端 embed + 配置加载 + 服务状态 API
- 🔨 进行中：进程管理完善、业务 API 迁移

## 结构

```
wechat-ai-panel-go/
├── cmd/server/
│   ├── main.go          入口 (embed web + http)
│   └── web/             前端构建产物 (从旧项目拷贝)
├── internal/
│   ├── config/          配置加载 (BOM/深合并/local)
│   ├── process/         进程管理 (端口/PID/树杀/健康)
│   └── api/             HTTP 路由
├── web/                 前端源 (构建产物缓存)
└── config.local.json    本机配置覆盖
```

## 开发运行

```bash
# 1. 拷贝最新前端构建 (从旧项目 static)
cp -r ../wechat-ai-panel/static/* cmd/server/web/

# 2. 运行
go run ./cmd/server        # 默认 8081 (config.local.json)

# 3. 访问
http://localhost:8081
```

## 构建单 exe

```bash
go build -o bin/wechat-ai-panel.exe ./cmd/server
```

## 计划

- [x] 阶段0: 契约冻结 (docs/REFACTOR_SPEC_BASELINE.md)
- [x] 阶段1: 骨架 + embed + /api/status
- [ ] 阶段1b: 进程启动/停止/树杀 + 健康检查
- [ ] 阶段2: 配置 BOM 原子写 / 日志读取 / hashId
- [ ] 阶段3: 业务 API 1:1 (install/whitelist/messages/qr/backups)
- [ ] 阶段4: 单文件分发

## 说明

- 面板本身为"命令壳 + 监控"，AstrBot(Python)/wechat-bot(Node) 作为外部进程由面板拉起
- 与旧 Python 面板可并行 (不同端口, 共享磁盘状态)
