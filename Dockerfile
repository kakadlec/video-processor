# Multi-stage build: `builder` compiles static binaries from the committed
# go.sum (fails closed on any drift), `test` adds ffmpeg on top of it so the
# integration suite can run, and `runtime` (the default build target) ships
# only the compiled binaries and ffmpeg — no Go toolchain, no source tree —
# as a non-root user.
#
# One image carries both the API and the worker, selected by the command a
# service runs. They share every dependency, and building two images from one
# source tree would only create a way for the two halves of a cutover to be
# at different commits.

FROM golang:1.27-alpine AS builder
ENV GOFLAGS=-mod=readonly
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app ./cmd/api \
    && CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

FROM builder AS test
RUN apk add --no-cache ffmpeg

FROM alpine:3.24 AS runtime
# ffmpeg is here for the worker, which is the only process that shells out to
# it now. It stays in the one shared image because the API and the worker ship
# together.
RUN apk add --no-cache ffmpeg \
    && adduser -D -u 1000 appuser
WORKDIR /app
# temp/ is the worker's scratch directory. It creates it at startup too; this
# is what makes it writable by the non-root user in the first place.
RUN mkdir -p temp && chown -R appuser:appuser /app
COPY --from=builder /out/app /app/app
COPY --from=builder /out/worker /app/worker
USER appuser
# The API's port. The worker listens on nothing — it is reached only through
# the broker — so a service running /app/worker publishes no port.
EXPOSE 8080
CMD ["/app/app"]
