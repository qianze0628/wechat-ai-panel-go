<div align="center">

# 🤖 WeChat AI Panel (Go)

**微信 AI 机器人管理面板 · Go 原生重构版**

单文件可执行、低资源占用、开箱即用的微信 AI 机器人一站式管理面板。
管理 [AstrBot](https://github.com/Soulter/AstrBot)（AI 对话引擎）与
[wechat-bot](https://github.com/qianze0628/wechat-bot-optimized)（Wechaty 微信桥接器），
提供 Web 可视化界面完成部署、配置、白名单、日志、备份等全部运维工作。

[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=white)](https://react.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](#license)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey)](#)

---

</div>

## ✨ 特性

- **🚀 一键部署**：引导式部署向导，自动检测/安装 Node、Python、AstrBot、微信桥接器，扫码即用
- **🖥️ 现代化 Web UI**：React 19 + Vite + Tailwind + HeroUI + Framer Motion，亮/暗主题 + DIY 配色
- **📦 单文件分发**：Go 原生编译，前端资源全部 `embed` 进可执行文件，无需运行时依赖
- **👥 白名单管理**：真人/公众号/群分类管理，群内成员级排除，私聊/群聊白名单分离
- **🔐 面板认证**：可选密码认证（Cookie 会话），配置热更新即时生效
- **📜 实时日志**：SSE 流式推送 wechat-bot / AstrBot / qr-server 日志，无需翻文件
- **💬 消息记录**：基于本地 `messages.jsonl` 的消息检索（按联系人/群/关键词）
- **💾 自动备份**：AstrBot 配置写入前自动快照，支持一键恢复
- **🔌 插件架构**：依赖安装引擎、消息记录等插件化扩展

## 🏗️ 架构

```
┌─────────────────────────────────────────────────────────────┐
│                    WeChat AI Panel (Go)                     │
│                                                             │
│   ┌──────────┐   ┌──────────┐   ┌──────────────────────┐   │
│   │  Web UI  │──▶│  REST    │   │  服务编排 (process)   │   │
│   │ React 19 │   │  net/http│   │ 启动/停止/重启/健康   │   │
│   └──────────┘   └────┬─────┘   └──────────┬───────────┘   │
│                       │                    │               │
│        ┌──────────────┼────────────────────┼──────────┐    │
│        ▼              ▼                    ▼          │    │
│   ┌─────────┐   ┌──────────┐   ┌─────────────────┐    │    │
│   │  AstrBot│   │ wechat-  │   │   qr-server     │    │    │
│   │ Python  │   │ bot(Node)│   │   (扫码登录)     │    │    │
│   │  :6185  │   │  :6189   │   │     :8090       │    │    │
│   └─────────┘   └──────────┘   └─────────────────┘    │    │
│        ▲            ▲                                 │    │
│        └── OneBot v11 WS (:20129) ──┘                 │    │
└───────────────────────────────────────────────────────┘
```

**数据链路**：面板 `/api/whitelist` → AstrBot 插件 `whitelist_manager` → wechat-bot `.env`（`ALIAS_WHITELIST` / `ROOM_WHITELIST` / `ROOM_MEMBER_EXCLUDE`）→ 桥接器拦截/放行。

## 📁 项目结构

```
wechat-ai-panel-go/
├── cmd/server/            # 入口: HTTP 服务 + 前端 embed
│   ├── main.go
│   └── web/               # 前端构建产物 (embed)
├── internal/
│   ├── api/               # HTTP 路由 (REST / SSE / 认证 / 设置)
│   ├── config/            # 配置加载 (BOM 兼容 / local 覆盖 / 热更新)
│   ├── process/           # 进程管理 (端口/PID/树杀/健康检查)
│   ├── system/            # 系统监控 (CPU/内存/磁盘/进程数)
│   └── util/              # 工具 (原子写 / hashId / 文件读取)
├── web/                   # 前端构建产物缓存 (提交到仓库, 保底可用)
├── config.json            # 面板配置 (本地, 不入库)
└── config.local.json      # 本地覆盖 (不入库)
```

## 🚀 快速开始

### 环境要求

- Go 1.26+（仅编译时需要；运行只需发布产物）
- 可选：Node.js 18+ / Python 3.10+（AstrBot 与桥接器运行依赖，面板可引导安装）

### 编译运行

```bash
# 1. 准备前端构建产物 (从配套前端项目拷贝)
cp -r ../wechat-ai-panel/static/* cmd/server/web/

# 2. 编译
go build -o bin/wechat-ai-panel.exe ./cmd/server

# 3. 运行 (默认端口 8081, 见 config.local.json)
./bin/wechat-ai-panel.exe

# 4. 访问
http://localhost:8081
```

### 使用引导

1. 打开面板 → **部署向导**：检测环境 → 一键安装缺失组件
2. **服务中心**：启动 AstrBot / wechat-bot / qr-server（可分别重启）
3. **连接配置**：扫码登录微信 → 一键配置 OneBot 桥接 → 打开 AstrBot WebUI
4. **白名单与管理员**：勾选可聊天的联系人/群，保存后自动同步并重启桥接器

## 📡 API 一览

| 分组 | 端点 | 说明 |
|---|---|---|
| 状态 | `GET /api/status` `/api/env` `/api/system` `/api/services` | 环境/服务/系统监控 |
| 服务 | `POST /api/start` `/api/stop` `/api/restart` | 服务控制（`?service=astrbot\|wechat\|qr`） |
| 白名单 | `GET/POST /api/whitelist` `/api/whitelist/contacts` `/api/whitelist/super` | 白名单/联系人/超管 |
| AstrBot | `GET /api/astrbot/creds` `/api/astrbot/setup/preview` `POST /api/astrbot/setup` `/api/astrbot/restore` | 凭据/OneBot 配置/恢复 |
| 监控 | `GET /api/logs` `GET /api/logs/stream` `GET /api/messages` | 日志（SSE 流）/消息记录 |
| 备份 | `GET /api/backups` | 备份列表 |
| 设置 | `GET/POST /api/settings` | 认证/备份开关（热更新） |
| 认证 | `POST /api/auth/login` `GET /api/auth/status` | 面板登录/状态 |
| 安装 | `POST /api/install` `GET /api/install/status` | 依赖安装引擎 |
| 二维码 | `GET /api/qr/status` | 微信登录状态 |
| 代理 | `GET /astrbot` | 302 跳转 AstrBot WebUI |

## 🧰 关联项目

| 项目 | 说明 |
|---|---|
| [wechat-ai-panel](https://github.com/qianze0628/wechat-ai-panel) | 本项目的 Python/FastAPI 原版（端口 8080） |
| [wechat-bot-optimized](https://github.com/qianze0628/wechat-bot-optimized) | Wechaty + wechat4u 微信桥接器（端口 6189） |
| [AstrBot](https://github.com/Soulter/AstrBot) | AI 对话引擎（WebUI 6185 / OneBot WS 20129） |

## ⚠️ 注意事项

- **非官方 API**：本工具基于 wechat4u 逆向协议登录个人微信，仅限个人学习研究使用
- **封号风险**：使用非官方微信协议存在账号风险，请自行评估并承担后果
- **并发运行**：Python 版（8080）与 Go 版（8081）可并存，共享磁盘状态，但同一时刻只应运行一个面板实例管理同一套服务

## 📄 License

[MIT](LICENSE) © 2026 qianze0628
