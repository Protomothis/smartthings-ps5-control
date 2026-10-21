# syntax=docker/dockerfile:1
# Single static binary on a scratch image. No libc, no shell: the raw HCI
# socket and UDP probe only need the kernel, so the final image is a few MB.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/ps5ctl ./cmd/ps5ctl

FROM scratch
COPY --from=build /out/ps5ctl /ps5ctl
# /data will hold config.json once the HTTP server lands.
VOLUME ["/data"]
ENTRYPOINT ["/ps5ctl"]
CMD ["help"]
