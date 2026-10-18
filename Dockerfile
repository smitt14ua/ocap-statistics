# Cross-compiles on the build platform (pure Go, no cgo), so multi-arch images build fast.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG DATE=
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
      -o /out/ocap-stats ./cmd/ocap-stats

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 ocap \
 && mkdir /data && chown ocap /data
USER ocap
ENV OCAPSTATS_LISTEN=0.0.0.0:5080 \
    OCAPSTATS_DB=/data/stats.db
VOLUME /data
EXPOSE 5080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:5080/api/v1/health || exit 1
ENTRYPOINT ["ocap-stats", "-config", "/config.json"]

# CI compiles the binaries outside Docker, where the Go build cache survives between runs, and
# only copies them in: --target prebuilt with dist/ocap-stats-linux-<arch> in the context.
FROM runtime AS prebuilt
ARG TARGETARCH
COPY dist/ocap-stats-linux-${TARGETARCH} /usr/local/bin/ocap-stats

# The default target builds from source.
FROM runtime
COPY --from=build /out/ocap-stats /usr/local/bin/ocap-stats
