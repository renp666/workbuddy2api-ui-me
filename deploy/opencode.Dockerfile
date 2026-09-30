# OpenCode 免费模型旁路 sidecar（OW Bridge）。
# 基础镜像用 Debian（glibc）：OW Bridge 首启会从 npm 下载 opencode-ai 官方二进制
# （Bun 编译，仅 glibc 构建），musl/alpine 无法运行。
FROM node:22-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && git clone --depth 1 https://github.com/louchi1984-coder/ow-bridge.git /app \
    && cd /app \
    && npm install --omit=dev --ignore-scripts \
    && apt-get purge -y git \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/* /app/.git
WORKDIR /app
COPY deploy/opencode-entrypoint.sh /usr/local/bin/opencode-entrypoint
RUN sed -i 's/\r$//' /usr/local/bin/opencode-entrypoint \
    && chmod +x /usr/local/bin/opencode-entrypoint
# BUDDY_NO_SYNC=1：不写宿主 ~/.workbuddy/models.json，模型列表只经 /v1/models 暴露。
ENV BUDDY_PORT=41980
ENV BUDDY_DATA_DIR=/data
ENV BUDDY_NO_SYNC=1
ENV HOME=/data
ENV WB2A_OPENCODE_KEY=""
EXPOSE 41980
ENTRYPOINT ["/usr/local/bin/opencode-entrypoint"]
