# The official image of the PortaPixel fleet server.
#
# Build it from the root of the repository:
#   docker build -f deploy/Dockerfile -t portapixel-server .
#
# The build stage makes a static binary. CGO is off, so the SQLite driver is the
# pure-Go one and the result needs no libc (D7).

FROM golang:1.25-alpine AS build

# git is not needed: the build has no dependency that comes from a repository.
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=linux
RUN GOARCH=${TARGETARCH:-amd64} go build \
        -trimpath \
        -ldflags "-s -w -X github.com/ethanpil/portapixel/internal/version.Version=${VERSION}" \
        -o /out/portapixel-server ./cmd/portapixel-server

# The run stage. Distroless "static" holds the CA certificates and the time zone
# database and nothing else: no shell, no package manager, no libc.
#
# The certificates are what the release list needs to reach GitHub. The time zone
# database is what a schedule needs, because a server that runs in a container has
# no /usr/share/zoneinfo of its own.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/portapixel-server /portapixel-server

# The image runs as the "nonroot" user of the base image, which is 65532. The
# volume must belong to that user:
#   docker run -v portapixel-data:/data ...   (docker makes it with the right owner)
#   chown -R 65532:65532 /your/host/directory (a bind mount needs this)
USER nonroot:nonroot

VOLUME /data
EXPOSE 8080

# The check asks the server for itself. The image holds no curl and no wget, so
# the binary does the work (portapixel-server selftest --url).
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s --retries=3 \
    CMD ["/portapixel-server", "selftest", "--url", "http://127.0.0.1:8080"]

ENTRYPOINT ["/portapixel-server"]
CMD ["run", "--data", "/data", "--listen", ":8080"]
