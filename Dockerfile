# Multi-stage build: `builder` compiles static binaries from the committed
# go.sum (fails closed on any drift), `test` adds ffmpeg on top of it so the
# integration suite can run, and `runtime` (the default build target) ships
# only the compiled binaries and ffmpeg — no Go toolchain, no source tree —
# as a non-root user.
#
# One image carries every entrypoint — the Identity API, the Video API, the
# Notification API, the worker and the notifier — selected by the command a
# service runs. They share every internal package, and building separate
# images from one source tree would only create a way for parts of one
# cutover to be at different commits.

# Pinned to the BUILD platform, never the target: CGO_ENABLED=0 makes every
# binary cross-compilable exactly, so the toolchain runs natively and only
# GOARCH changes. Running the Go toolchain under emulation instead would cost
# minutes per platform for an identical result.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
# Supplied by BuildKit. Empty on a builder that does not set it, which makes
# GOARCH= a no-op and yields a native build — the same thing this produced
# before there were two platforms.
ARG TARGETARCH
ENV GOFLAGS=-mod=readonly
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# GOARCH belongs on ALL FIVE. Reaching four of them produces an image that
# builds, scans and pushes clean, with one process that dies at exec on one
# platform only — which is why CI reads the ELF headers rather than trusting
# that this line stayed complete.
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o /out/identity-api ./cmd/identity-api \
    && CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o /out/video-api ./cmd/video-api \
    && CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o /out/notification-api ./cmd/notification-api \
    && CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o /out/notifier ./cmd/notifier

# Inherits the builder's BUILDPLATFORM pin, which is required rather than
# incidental: this stage backs `docker compose run --build --rm app-test`, and
# an emulated Go toolchain would make the documented local test path unusable.
FROM builder AS test
RUN apk add --no-cache ffmpeg

FROM alpine:3.24 AS runtime
# ffmpeg is here for the worker, which is the only process that shells out to
# it now — none of the HTTP services nor the notifier needs it. It stays in
# the one shared image because they all ship together.
RUN apk add --no-cache ffmpeg \
    && adduser -D -u 1000 appuser
WORKDIR /app
# temp/ is the worker's scratch directory. It creates it at startup too; this
# is what makes it writable by the non-root user in the first place.
RUN mkdir -p temp && chown -R appuser:appuser /app
COPY --from=builder /out/identity-api /app/identity-api
COPY --from=builder /out/video-api /app/video-api
COPY --from=builder /out/notification-api /app/notification-api
COPY --from=builder /out/worker /app/worker
COPY --from=builder /out/notifier /app/notifier
USER appuser
# Every HTTP service in this image listens here; none of them publishes it,
# because the gateway is the only service that publishes a host port. The
# worker and the notifier listen on nothing — each is reached only through
# the broker.
EXPOSE 8080
# The Video API, because it is the service that serves the frontend and is
# therefore the least surprising thing a bare `docker run` should start.
# Every other process is named explicitly by whatever runs it.
CMD ["/app/video-api"]
