# Multi-stage build: `builder` compiles a static binary from the committed
# go.sum (fails closed on any drift), `test` adds ffmpeg on top of it so the
# integration suite can run, and `runtime` (the default build target) ships
# only the compiled binary and ffmpeg — no Go toolchain, no source tree — as
# a non-root user.

FROM golang:1.26-alpine AS builder
ENV GOFLAGS=-mod=readonly
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app ./cmd/api

FROM builder AS test
RUN apk add --no-cache ffmpeg

FROM alpine:3.24 AS runtime
RUN apk add --no-cache ffmpeg \
    && adduser -D -u 1000 appuser
WORKDIR /app
RUN mkdir -p uploads temp && chown -R appuser:appuser /app
COPY --from=builder /out/app /app/app
USER appuser
EXPOSE 8080
CMD ["/app/app"]
