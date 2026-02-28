# ── Stage 1: Build ───────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache deps first (layer cache friendly)
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /quoteflow ./cmd/server/main.go

# ── Stage 2: Runtime ─────────────────────────────────────────
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /quoteflow /quoteflow

EXPOSE 8080

ENTRYPOINT ["/quoteflow"]
