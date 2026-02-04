# Build go
FROM --platform=$BUILDPLATFORM golang:1.25.0-alpine AS builder
WORKDIR /app
COPY . .
ENV CGO_ENABLED=0

RUN apk --no-cache add git ca-certificates

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

ARG GOPROXY=https://goproxy.cn|https://proxy.golang.org|direct
ARG GOSUMDB=off
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}

RUN set -e; \
    for i in 1 2 3; do \
      GOEXPERIMENT=jsonv2 go mod download && break; \
      echo "go mod download failed (attempt $i), retrying..."; \
      sleep 2; \
    done
RUN GOEXPERIMENT=jsonv2 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -v -o V2bX -tags "sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor"

FROM scratch AS artifact
COPY --from=builder /app/V2bX /V2bX

# Release
FROM --platform=$TARGETPLATFORM alpine
# 安装必要的工具包
RUN  apk --update --no-cache add tzdata ca-certificates \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
RUN mkdir /etc/V2bX/
COPY --from=builder /app/V2bX /usr/local/bin

ENTRYPOINT [ "V2bX", "server", "--config", "/etc/V2bX/config.json"]
