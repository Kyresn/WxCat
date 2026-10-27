# syntax=docker/dockerfile:1

# WxCat gateway image: OneBot 11 + WebUI only, no Wine/WeChat.
# Multi-arch: linux/amd64, linux/arm64.
#
# The WeChat backend (WeChatFerry over Wine, or MimicWX) runs elsewhere and is
# reached over the network. See docs/arm64.md and docs/backend-wcf.md.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
        -ldflags "-s -w -X wxcat/internal/onebot.Version=${VERSION}" \
        -o /out/wxcat ./cmd/wxcat

FROM alpine:3.24

ARG VERSION=dev
LABEL org.opencontainers.image.title="WxCat gateway" \
      org.opencontainers.image.description="WeChat to OneBot 11 gateway" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata tini su-exec \
    && adduser -D -u 1000 wxcat \
    && mkdir -p /app/config /app/data \
    && chown -R wxcat:wxcat /app

COPY --from=build /out/wxcat /usr/local/bin/wxcat
COPY config.example.json /opt/wxcat/config.example.json
COPY docker/gateway-entrypoint.sh /usr/local/bin/gateway-entrypoint.sh
RUN chmod +x /usr/local/bin/gateway-entrypoint.sh

ENV WXCAT_CONFIG=/app/config/wxcat.json \
    WXCAT_DATA=/app/data \
    WXCAT_BACKEND=wcf

WORKDIR /app

EXPOSE 3000 3001 6099
VOLUME ["/app/config", "/app/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:6099/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/gateway-entrypoint.sh"]
