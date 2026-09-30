FROM node:20-alpine
RUN apk add --no-cache git \
    && npm install -g @qodercn-ai/qoderclicn \
    && git clone --depth 1 https://github.com/avaritiachaos/qoder-proxy.git /app \
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
