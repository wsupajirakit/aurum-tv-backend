# syntax=docker/dockerfile:1

# ---------- build: static binaries, no cgo ----------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/aurum-api ./cmd/aurumctl

# ---------- run ----------
FROM alpine:3
# su-exec drops root after the data volume has been chowned; ca-certificates for outbound https
RUN apk add --no-cache ca-certificates su-exec tzdata \
 && addgroup -g 1000 -S app && adduser -u 1000 -S -G app -H -s /sbin/nologin app
COPY --from=build /out/aurum-api /out/aurumctl /usr/local/bin/
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

ENV APP_ENV=production \
    PORT=3000 \
    DATA_DIR=/data
# Data lives outside the image: mount a volume at /data and redeploys keep every channel.
VOLUME ["/data"]
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["aurum-api", "healthcheck"]
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["aurum-api"]
