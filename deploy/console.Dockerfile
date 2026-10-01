FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
WORKDIR /source
COPY console ./
ARG TARGETOS
ARG TARGETARCH
# 默认与 Go 自身默认一致；网络受限环境可通过 build args 覆盖为镜像源。
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
RUN go test ./... && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/console .

FROM alpine:3.20
RUN test "$(apk --print-arch)" = x86_64 \
    && apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 10001 app \
    && mkdir -p /run/wb2a /app/console-data \
    && chown -R app:app /run/wb2a /app/console-data
COPY --from=build /out/console /app/console
COPY LICENSE /app/LICENSE
ENV WB2A_CORE_URL=http://core:7863 WB2A_LISTEN=:7863 WB2A_KEY_FILE=/run/wb2a/keys.json \
    WB2A_ROUTE_FILE=/app/console-data/routes.json
USER app
EXPOSE 7863
ENTRYPOINT ["/app/console"]
