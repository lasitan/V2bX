# Build go
FROM --platform=$BUILDPLATFORM golang:1.25.0-alpine AS builder
WORKDIR /app
COPY . .
ENV CGO_ENABLED=0
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG BUILDPLATFORM
ARG PROXY=https://proxy.golang.org,https://goproxy.cn,direct
ENV GOPROXY=${PROXY}
ARG GOSUMDB=off
ENV GOSUMDB=${GOSUMDB}
ENV GODEBUG=http2client=0

RUN set -eux; \
    n=0; \
    until [ "$n" -ge 5 ]; do \
      go mod download && break; \
      n=$((n+1)); \
      echo "go mod download failed, retry ${n}/5"; \
      sleep $((n*2)); \
    done; \
    [ "$n" -lt 5 ]
RUN set -eux; \
    go mod download github.com/sagernet/wireguard-go github.com/sagernet/gvisor || true; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-mod=mod go build -v -o V2bX -tags "sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor"

# Release
FROM  alpine
# 安装必要的工具包
RUN  apk --update --no-cache add tzdata ca-certificates \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
RUN mkdir /etc/V2bX/
COPY --from=builder /app/V2bX /usr/local/bin

ENTRYPOINT [ "V2bX", "server", "--config", "/etc/V2bX/config.json"]
