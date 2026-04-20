# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Cache dependencies separately from source
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-s -w -X main.lumindashVersion=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" \
    -trimpath \
    -o /lumindash \
    ./cmd/lumindash

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM alpine:3.19

# ca-certificates: needed for HTTPS calls to the Zabbix JSON-RPC API
# tzdata: needed for correct time zone handling in logs / timestamps
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S lumindash && \
    adduser  -S -G lumindash lumindash

COPY --from=builder /lumindash /usr/local/bin/lumindash

USER lumindash

EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8090/healthz | grep -q '"status":"ok"' || exit 1

ENTRYPOINT ["/usr/local/bin/lumindash"]
