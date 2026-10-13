# syntax=docker/dockerfile:1
#
# 多阶段构建：前端静态导出 → Go 单二进制（内嵌前端）→ 运行镜像。
# 原 Docker Hub 镜像源（autumn27/artex）已失效，本仓库自此支持从纯源码自助构建：
#   docker compose build        # 或 docker build -t artex:local .
# TARGETARCH 由 buildx 自动注入（amd64/arm64），Go 交叉编译无需 QEMU；
# 运行层只装工具，编译全部在构建阶段完成。
# 国内网络可覆盖模块源（默认已对国内友好）：
#   --build-arg GOPROXY=https://goproxy.cn,direct
#   --build-arg NPM_REGISTRY=https://registry.npmmirror.com

# 发布工作流复用同一提交生成的 Release 二进制，普通源码构建仍使用 gobuild。
ARG BINARY_STAGE=gobuild

########## 阶段 1：前端静态导出 ##########
FROM node:20-bookworm AS web
ARG NPM_REGISTRY=https://registry.npmmirror.com
WORKDIR /web
# --ignore-scripts：跳过 prepare(husky)——容器里没有 .git，husky 会失败；
# Next.js/Biome 的平台二进制走 optionalDependencies，不依赖生命周期脚本。
COPY web/package.json web/package-lock.json ./
RUN npm config set registry "$NPM_REGISTRY" && npm ci --ignore-scripts
COPY web/ ./
RUN NEXT_EXPORT=1 NEXT_PUBLIC_MOCK=0 npx next build --webpack

########## 阶段 2：Go 后端（内嵌前端，静态编译）##########
FROM golang:1.26-bookworm AS gobuild
ARG TARGETARCH
ARG ARTEX_BUILD_VERSION=dev
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
COPY norma/ ./norma/
RUN go mod download
COPY . .
# 前端静态产物从 web 阶段拷入（.dockerignore 已排除宿主 dist，不会冲突）
COPY --from=web /web/out/ server/webui/dist/
RUN CGO_ENABLED=0 GOARCH="${TARGETARCH}" go build -tags embedui -trimpath \
    -ldflags "-s -w -X main.version=${ARTEX_BUILD_VERSION}" -o /out/artex ./cmd/artex

FROM scratch AS prebuilt
ARG TARGETARCH
COPY dist/${TARGETARCH}/artex /out/artex

FROM ${BINARY_STAGE} AS binary

########## 阶段 3：运行镜像（同原版：工具 + Playwright 预装）##########
FROM python:3.12-slim-bookworm
ARG TARGETARCH
ARG NPM_REGISTRY=https://registry.npmmirror.com
# 浏览器使用 Playwright 官方默认下载路径，避免镜像站缺少新版本资产。
ENV npm_config_registry=$NPM_REGISTRY
# 常用工具：ripgrep / curl / vim，加一批 recon 常备件（按需增删）。
# Node 从 NodeSource 装 20.x：bookworm 自带的 apt nodejs 是 18，Playwright 要求 >=20。
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip ffmpeg \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# CLI、MCP 和浏览器固定为同一依赖图；默认浏览器配置由任务包装器注入。
# 页面更新后的程序也携带相同锁文件，可在容器数据目录补齐工具包。
COPY runtime/playwright/package*.json /opt/artex-playwright/
RUN npm ci --prefix /opt/artex-playwright --ignore-scripts \
    && /opt/artex-playwright/node_modules/.bin/playwright install --with-deps chromium ffmpeg \
    && ln -s /opt/artex-playwright/node_modules/.bin/playwright /usr/local/bin/playwright \
    && ln -s /opt/artex-playwright/node_modules/.bin/playwright-mcp /usr/local/bin/playwright-mcp \
    && rm -rf /var/lib/apt/lists/*
COPY runtime/playwright/playwright-cli.mjs /usr/local/bin/artex-playwright-cli
RUN chmod 0755 /usr/local/bin/artex-playwright-cli \
    && ln -s /usr/local/bin/artex-playwright-cli /usr/local/bin/playwright-cli \
    && playwright-cli --help
# 固定版本及 SHA256 校验 wgcf/wireproxy，避免运行未验证的工具资产。
COPY egress/install.py egress/dependencies.json /tmp/wgcf-warp/
RUN python /tmp/wgcf-warp/install.py "${TARGETARCH}" /usr/local/bin \
    && chmod 0755 /usr/local/bin/wgcf /usr/local/bin/wireproxy \
    && rm -rf /tmp/wgcf-warp
WORKDIR /app
# 构建阶段产出的单二进制（内嵌前端）
COPY --from=binary /out/artex /app/artex
# 守护启动脚本：进程退出后按退出码决定是否重新拉起，页面一键更新靠它完成换装。
# 它同时负责把 SIGTERM 转发给 artex —— docker stop 只把信号发给 PID 1，
# 不转发的话 artex 收不到、做不了优雅关闭，10 秒后被 SIGKILL 硬杀。
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/（SQLite + jwt.key）持久化点
VOLUME ["/app/data", "/app/proxy-secrets"]
ENV ARTEX_PROXY_SECRETS_FILE=/app/proxy-secrets/key
ENV ARTEX_BASE_DIR=/app ARTEX_SELFUPDATE_ROOT=/app/data/self-update
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
