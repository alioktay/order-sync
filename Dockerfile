# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY 'cmd' './cmd'
COPY internal ./internal
COPY migrations ./migrations
COPY docs ./docs
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/order-sync ./cmd/order-sync
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/order-sync-admin ./cmd/order-sync-admin

FROM alpine:3.22
WORKDIR /app
COPY --from=build /out/order-sync /app/order-sync
COPY --from=build /out/order-sync-admin /app/order-sync-admin
USER nobody
ENTRYPOINT ["/app/order-sync"]
