# Multi-stage build for sweep-mcp.
#
# 1. golang:1.26 alpine builds a static binary
# 2. scratch holds the final ~12 MB image
#
# Same shape as tor-character-mcp-docker. The binary has no external
# runtime deps; everything (the n8n client, JSON-schema wiring) is
# baked into the Go binary at compile time.

# ---- 1. build ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux \
    go build \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -trimpath \
        -o /out/sweep-mcp \
        ./cmd/sweep-mcp

# ---- 2. final ----
FROM scratch
COPY --from=build /out/sweep-mcp /sweep-mcp
EXPOSE 8080
ENTRYPOINT ["/sweep-mcp"]
CMD ["--http=:8080"]
