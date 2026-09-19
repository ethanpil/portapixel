# The official image of the PortaPixel fleet server.
#
# Build it from the root of the repository:
#   docker build -f deploy/Dockerfile -t portapixel-server .
#
# The build stage makes a static binary. CGO is off, so the SQLite driver is the
# pure-Go one and the result needs no libc (D7).
#
# "--platform=$BUILDPLATFORM" is NOT optional. Without it buildx runs the whole
# build stage on the TARGET platform. The arm64 build then compiles Go under
# qemu-user emulation, which was most of a CI job of 7.5 minutes, and the ARG
# TARGETARCH below did nothing at all. With it the compiler runs on the machine of
# the runner and cross-compiles. A Go build with CGO off needs no file of the
# target platform.

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

# git is not needed: the build has no dependency that comes from a repository.
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
# buildx sets these two for each platform of the build. The defaults are for a
# plain "docker build", which sets neither.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags "-s -w -X github.com/ethanpil/portapixel/internal/version.Version=${VERSION}" \
        -o /out/portapixel-server ./cmd/portapixel-server

# An empty directory to copy into the run stage, so /data exists in the image
# with the right owner. See the note in the run stage.
RUN mkdir -p /out/data

# The run stage. Distroless "static" holds the CA certificates and the time zone
# database and nothing else: no shell, no package manager, no libc.
#
# The certificates are what the release list needs to reach GitHub. The time zone
# database is what a schedule needs, because a server that runs in a container has
# no /usr/share/zoneinfo of its own.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/portapixel-server /portapixel-server

# /data MUST exist in the image, and it must belong to the user that runs.
# Docker copies the owner and the mode of the path in the IMAGE onto a new named
# volume. With no such path the volume is made as root, the server runs as 65532,
# and the first write of server.toml fails with "permission denied". The
# container then restarts for ever and the documented Docker path never works.
# A bind mount takes its owner from the host, so that case still needs:
#   chown -R 65532:65532 /your/host/directory
COPY --from=build --chown=65532:65532 /out/data /data

# The "nonroot" user of the base image is 65532.
USER 65532:65532

VOLUME /data
EXPOSE 8080

# The check asks the server for itself. The image holds no curl and no wget, so
# the binary does the work.
#
# "--url auto" reads the listen address of the configuration. A person who changes
# --listen or PORTAPIXEL_LISTEN does not also have to change this line.
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s --retries=3 \
    CMD ["/portapixel-server", "selftest", "--data", "/data", "--url", "auto"]

ENTRYPOINT ["/portapixel-server"]
CMD ["run", "--data", "/data", "--listen", ":8080"]
