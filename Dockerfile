# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Download dependencies first (cached unless go.mod/go.sum change)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o datavault .

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM alpine:3.19

# Install runtime deps + mongodb-database-tools (provides mongodump / mongorestore)
RUN apk add --no-cache ca-certificates tzdata && \
    apk add --no-cache --repository=https://dl-cdn.alpinelinux.org/alpine/edge/community \
    mongodb-tools

WORKDIR /app
COPY --from=builder /src/datavault .

VOLUME ["/data"]
EXPOSE 8000

CMD ["/app/datavault"]
