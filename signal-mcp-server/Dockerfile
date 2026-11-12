# syntax=docker/dockerfile:1

# ---- build stage ---------------------------------------------------------
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Copied separately from the rest of the source so dependency resolution is
# cached independently of source changes. This project currently has zero
# external dependencies (stdlib only), but this keeps the Dockerfile correct
# if that ever changes.
COPY go.mod ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/signal-mcp-server .

# ---- runtime stage --------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    addgroup -S app && adduser -S -G app app

WORKDIR /app
COPY --from=builder /out/signal-mcp-server ./signal-mcp-server

USER app

EXPOSE 8081

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8081/healthz || exit 1

ENTRYPOINT ["./signal-mcp-server"]
