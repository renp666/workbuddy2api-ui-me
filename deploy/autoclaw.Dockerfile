# AutoClaw 旁路 sidecar（autoclaw2api Python 桥接）。
# 源码取自参考实现 autoclaw2api（bridge.py + check.py），仅监听 127.0.0.1，
# 由 console 经共享网络命名空间访问。
FROM python:3.12-slim
WORKDIR /app
RUN pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple httpx websockets
COPY deploy/autoclaw2api/bridge.py deploy/autoclaw2api/check.py deploy/autoclaw2api/console.html /app/
COPY deploy/autoclaw2api/config.json /app/config.json
COPY deploy/autoclaw-entrypoint.sh /usr/local/bin/autoclaw-entrypoint
RUN mkdir -p /app/auths /app/data \
    && sed -i 's/\r$//' /app/bridge.py /app/check.py /usr/local/bin/autoclaw-entrypoint \
    && chmod +x /usr/local/bin/autoclaw-entrypoint
ENV BUDDY_DATA_DIR=/data
ENV WB2A_AUTOCLAW_KEY=""
EXPOSE 7865
ENTRYPOINT ["/usr/local/bin/autoclaw-entrypoint"]
