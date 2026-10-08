FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
WORKDIR /source
COPY deploy/raccoon2api ./
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
RUN go test ./... && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/raccoon2api .

FROM alpine:3.20
RUN test "$(apk --print-arch)" = x86_64 && apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app && mkdir -p /data/auths && chown -R app:app /data
COPY --from=build /out/raccoon2api /app/raccoon2api
COPY LICENSE /app/LICENSE
ENV RACCOON_LISTEN=127.0.0.1:7867 RACCOON_DATA_DIR=/data
USER app
EXPOSE 7867
ENTRYPOINT ["/app/raccoon2api"]