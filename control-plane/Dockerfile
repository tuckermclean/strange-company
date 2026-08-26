# Build stage: compile a static binary. A separate build stage keeps the Go
# toolchain, module cache, and source tree out of the final image.
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
COPY . .

# go.sum is intentionally not committed while dependencies are still moving,
# so resolve them here. This keeps the image build correct whether or not a
# go.sum is present in the build context.
RUN go mod tidy
ARG VERSION=dev
# CGO_ENABLED=0 produces a statically linked binary with no libc dependency,
# so it needs no writable filesystem and no OS packages to run - required for
# the distroless base below and for readOnlyRootFilesystem to work at all.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/control-plane ./cmd/control-plane

# Runtime stage: distroless/static has no shell, package manager, or libc -
# just enough to run a static binary - which minimizes attack surface and
# needs nothing writable, satisfying the chart's readOnlyRootFilesystem.
FROM gcr.io/distroless/static-debian12:nonroot
# UID/GID 65532 is the "nonroot" user baked into distroless images, matching
# the chart's runAsNonRoot/runAsUser/runAsGroup: 65532 requirement exactly.
USER 65532:65532
COPY --from=build /out/control-plane /control-plane
EXPOSE 8080
ENTRYPOINT ["/control-plane"]
