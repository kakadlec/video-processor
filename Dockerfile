# Multi-stage build: `builder` compiles static binaries from the committed
# go.sum (fails closed on any drift), `test` adds ffmpeg on top of it so the
# integration suite can run, and `runtime` (the default build target) ships
# only the compiled binaries and ffmpeg — no Go toolchain, no source tree —
# as a non-root user.
#
# One image carries all three entrypoints — the API, the worker, and the
# notifier — selected by the command a service runs. They share every
# internal package, and building separate images from one source tree would
# only create a way for parts of one cutover to be at different commits.

FROM golang:1.27-alpine AS builder
ENV GOFLAGS=-mod=readonly
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app ./cmd/api \
    && CGO_ENABLED=0 go build -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 go build -o /out/notifier ./cmd/notifier

FROM builder AS test
RUN apk add --no-cache ffmpeg

FROM alpine:3.24 AS runtime
# ffmpeg is here for the worker, which is the only process that shells out to
# it now — neither the API nor the notifier needs it. It stays in the one
# shared image because all three ship together.
RUN apk add --no-cache ffmpeg \
    && adduser -D -u 1000 appuser
WORKDIR /app
# temp/ is the worker's scratch directory. It creates it at startup too; this
# is what makes it writable by the non-root user in the first place.
RUN mkdir -p temp && chown -R appuser:appuser /app
COPY --from=builder /out/app /app/app
COPY --from=builder /out/worker /app/worker
COPY --from=builder /out/notifier /app/notifier
USER appuser
# The API's port. The worker and the notifier listen on nothing — each is
# reached only through the broker — so a service running /app/worker or
# /app/notifier publishes no port.
EXPOSE 8080
CMD ["/app/app"]
