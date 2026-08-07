# syntax=docker/dockerfile:1
# =============================================================================
# WeChat AI Panel (Go) - Docker 多阶段构建
#
# 运行镜像包含: 面板(Go) + Node(wechat-bot) + Python/uv(AstrBot)
# 面板负责拉起/管理 AstrBot 与 wechat-bot 进程。
#
# 构建:  docker build -t wechat-ai-panel .
# 运行:  docker compose up -d  (推荐, 见 docker-compose.yml)
# 或:    docker run -d -p 8081:8081 -v $PWD/data:/app/data wechat-ai-panel
# =============================================================================

# ---------- 阶段 1: 编译 Go 面板 ----------
FROM golang:1.26-alpine AS builder
WORKDIR /src

# 依赖缓存 (利用 Docker 层缓存)
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 静态编译; -ldflags 瘦身
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /out/wechat-ai-panel ./cmd/server

# ---------- 阶段 2: 运行镜像 ----------
# node 用于 wechat-bot; python+uv 用于 AstrBot (面板可引导安装)
FROM node:20-bookworm-slim AS runtime

# 安装 python3 / uv / 基础工具
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 python3-pip curl ca-certificates git \
    && curl -LsSf https://astral.sh/uv/install.sh | sh \
    && rm -rf /var/lib/apt/lists/*

ENV PATH="/root/.local/bin:${PATH}" \
    UV_LINK_MODE=copy

WORKDIR /app

# 复制面板二进制
COPY --from=builder /out/wechat-ai-panel /usr/local/bin/wechat-ai-panel

# 预置目录 (与默认配置相对路径对应)
RUN mkdir -p /app/wechat-bot-windows /app/.astrbot-root /app/.astrbot-data \
             /app/logs /app/runtime /app/cmd_config

# 示例配置 (容器内默认 0.0.0.0:8081, 便于外部访问)
COPY config.local.example.json /app/config.local.example.json
RUN cp /app/config.local.example.json /app/config.local.json

# 端口: 面板 8081 / AstrBot WebUI 6185 / OneBot WS 20129 / wechat-bot 6189 / qr 8090
EXPOSE 8081 6185 20129 6189 8090

# 数据卷: wechat-bot 源码(或挂载)、AstrBot 数据、配置
VOLUME ["/app/wechat-bot-windows", "/app/.astrbot-root", "/app/.astrbot-data", "/app/cmd_config"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8081/api/status || exit 1

CMD ["wechat-ai-panel"]
