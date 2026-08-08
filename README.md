<div align="center">

# 🤖 WeChat AI Panel

**微信 AI 机器人管理面板 · 官方主版本**

单文件可执行、性能出色、低资源占用、开箱即用的微信 AI 机器人一站式管理面板。
管理 [AstrBot](https://github.com/AstrBotDevs/AstrBot)（AI 对话引擎）与
[wechat-bot](https://github.com/qianze0628/wechat-bot-optimized)（Wechaty 微信桥接器），
提供 Web 可视化界面完成部署、配置、白名单、日志、备份等全部运维工作。

> 🚀 **为什么选择本版本**：Go 编译为单一可执行文件，无需安装 Python 环境即可运行面板本身；
> 启动快、内存占用低；Windows / Linux / macOS 均可直接使用预编译 Release，开箱即用。

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

> **跨平台说明**：面板本体（Go）与依赖安装（npm/uv）在 Windows / Linux / macOS 上无差异。
> 但 **wechat-bot 使用微信旧网页版协议（wechat4u），登录稳定性受微信风控影响，与操作系统无关**——
> Windows 桌面环境更接近普通用户，Linux 服务器 / Docker 容器环境特征更"非典型"，更容易触发风控掉线。
> 个人使用推荐 Windows；服务器场景如遇掉线可降低登录频率或考虑商业协议方案。

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

1. 打开面板 → **一键部署向导**：检测环境 → 一键安装缺失组件
2. **服务中心**：启动 AstrBot / wechat-bot / qr-server（可分别重启）
3. **连接配置**：扫码登录微信 → 一键配置 OneBot 桥接 → 打开 AstrBot WebUI
4. **白名单与管理员**：勾选可聊天的联系人/群，保存后自动同步并重启桥接器

### 🇨🇳 国内镜像加速 (免代理)

面板内置国内镜像源，安装依赖自动加速，**无需配置即可生效**：

| 阶段 | 默认镜像 | 说明 |
|---|---|---|
| npm install | `registry.npmmirror.com` | 淘宝 npm 镜像 |
| uv / pip | `mirrors.aliyun.com/pypi/simple/` | 阿里云 PyPI |
| git clone | 直连 GitHub | 可配加速前缀，默认关 |

可在 `config.local.json` 的 `mirrors` 段覆盖（留空 = 直连官方源）：

```json
"mirrors": {
  "npm_registry": "https://registry.npmmirror.com",
  "pypi_index": "https://mirrors.aliyun.com/pypi/simple/",
  "git_clone_proxy": ""
}
```

> `git_clone_proxy` 是 GitHub 加速前缀（ghproxy 类公共服务不稳定，失效时请更换其他
> 可用源，或留空直连）。`npm_registry` / `pypi_index` 走阿里云公共免费服务。

### 🩹 AstrBot 升级自动恢复

面板内置群聊上下文过滤补丁（防止群聊"答非所问"）。若 AstrBot 升级冲掉了补丁，
面板启动时自动检测并重新打上（`/api/patch/status` 可查状态，`/api/patch/reapply`
可手动重打），无需手动干预。

## 🐳 Docker 部署

镜像内置 Go 面板 + Node（wechat-bot）+ Python/uv（AstrBot），面板在容器内拉起并管理这些服务。

```bash
# 构建并启动
docker compose up -d --build

# 浏览器访问面板
# http://localhost:8081
```

数据持久化在 `./data/`（wechat-bot 源码 / AstrBot 数据 / 配置 / 日志）。
默认监听 `0.0.0.0:8081`（`config.local.example.json`），挂载自定义配置可覆盖：

```yaml
# docker-compose.yml 追加
volumes:
  - ./config.local.json:/app/config.local.json
```

> 也可直接用预编译二进制（见 [Releases](https://github.com/qianze0628/wechat-ai-panel-go/releases)），
> Windows / Linux / macOS 均支持，无需 Docker。

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
| [wechat-ai-panel](https://github.com/qianze0628/wechat-ai-panel) | 本项目的前身（Python/FastAPI 版，端口 8080，功能相同的轻量备选） |
| [wechat-bot-optimized](https://github.com/qianze0628/wechat-bot-optimized) | Wechaty + wechat4u 微信桥接器（端口 6189） |
| [AstrBot](https://github.com/AstrBotDevs/AstrBot) | AI 对话引擎（WebUI 6185 / OneBot WS 20129） |

## ⚠️ 注意事项

- **非官方 API**：本工具基于 wechat4u 逆向协议登录个人微信，仅限个人学习研究使用
- **封号风险**：使用非官方微信协议存在账号风险，请自行评估并承担后果
- **并发运行**：本版本（8081）与 Python 轻量版（8080）可并存，共享磁盘状态，但同一时刻只应运行一个面板实例管理同一套服务

## 📄 License

[MIT](LICENSE) © 2026 qianze0628
