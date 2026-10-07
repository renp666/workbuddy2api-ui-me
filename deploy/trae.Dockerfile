# Trae 旁路 sidecar（workbuddy2api-trae：TRAE SOLO 上游 OpenAI 兼容网关）。
# 纯 Go、无第三方依赖，仅监听 127.0.0.1，由 console 经共享网络命名空间访问。
# 源码移植自参考实现 AgentPoints 的 internal/trae/（MIT），入口壳为本仓自写。
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
WORKDIR /source
COPY deploy/trae2api ./
ARG TARGETOS
ARG TARGETARCH
# 默认与 Go 自身默认一致；网络受限环境可通过 build args 覆盖为镜像源。
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
RUN go test ./... && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/trae2api .

FROM alpine:3.20
RUN test "$(apk --print-arch)" = x86_64 \
    && apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app \
    && mkdir -p /data/auths \
    && chown -R app:app /data
COPY --from=build /out/trae2api /app/trae2api
COPY LICENSE /app/LICENSE
ENV TRAE_LISTEN=127.0.0.1:7866 TRAE_DATA_DIR=/data TRAE_CALLBACK_LISTEN=0
USER app
EXPOSE 7866
ENTRYPOINT ["/app/trae2api"]
