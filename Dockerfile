# syntax=docker/dockerfile:1.7

ARG BIN=api

FROM golang:1.25-alpine AS builder
ARG BIN
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${BIN}

FROM gcr.io/distroless/static-debian12:nonroot
USER nonroot:nonroot
COPY --from=builder /out/app /app
# BIN によって使うポートが変わる（api=8080 / grpc=9090。batch と outbox-worker は listen しない）。
# EXPOSE は宣言だけで実際の公開は docker run -p が決めるため、両方を挙げておく。
EXPOSE 8080 9090
ENTRYPOINT ["/app"]
