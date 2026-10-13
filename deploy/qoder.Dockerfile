FROM node:20-alpine
# 上游版本固定：两个来源都不跟随默认分支/最新版，避免上游改动让镜像「同名不同料」、
# 也让本仓的构建期补丁（relax-token-guard / fix-e2big-argv）有确定的上下文可依。
#   升级步骤：改下面的 tag/版本号 → 重建镜像 → 复核两个 .cjs 补丁仍能应用。
#   qoder-proxy    : https://github.com/avaritiachaos/qoder-proxy  (tag v1.6.0, 2026-09-05)
#   qoderclicn npm : @qodercn-ai/qoderclicn@1.1.69                (2026-10-12)
# GITHUB_MIRROR：github.com 被网络策略拦截时改用镜像（默认仍是官方地址，行为不变）。
#   语义是「/owner/repo 之前的完整前缀」，所以镜像要连 https://github.com 一起带上，例如：
#   docker compose build --build-arg GITHUB_MIRROR=https://ghfast.top/https://github.com qoder-proxy
ARG GITHUB_MIRROR=https://github.com
RUN apk add --no-cache git \
    && npm install -g @qodercn-ai/qoderclicn@1.1.69 \
    && git clone --depth 1 --branch v1.6.0 ${GITHUB_MIRROR}/avaritiachaos/qoder-proxy.git /app \
    && cd /app \
    && npm install --production \
    && apk del git
WORKDIR /app
# 本仓补丁与控制进程：支持设备码 OAuth 页面登录（见 deploy/qoder-login-ctl.cjs）；
# fix-e2big-argv 修复 Linux 下超长 --append-system-prompt 触发的 spawn E2BIG。
COPY deploy/qoder-relax-token-guard.cjs /tmp/relax-token-guard.cjs
COPY deploy/qoder-fix-e2big-argv.cjs /tmp/fix-e2big-argv.cjs
COPY deploy/qoder-login-ctl.cjs /opt/qoder-login-ctl.cjs
COPY deploy/qoder-entrypoint.sh /usr/local/bin/qoder-entrypoint
RUN node /tmp/relax-token-guard.cjs \
    && node /tmp/fix-e2big-argv.cjs \
    && rm /tmp/relax-token-guard.cjs /tmp/fix-e2big-argv.cjs \
    && sed -i 's/\r$//' /usr/local/bin/qoder-entrypoint /opt/qoder-login-ctl.cjs \
    && chmod +x /opt/qoder-login-ctl.cjs /usr/local/bin/qoder-entrypoint
ENV PORT=3000
ENV CLI_BACKEND=cn
ENV PROXY_API_KEY=""
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/qoder-entrypoint"]
