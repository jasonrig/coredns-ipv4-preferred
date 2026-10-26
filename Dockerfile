# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Execute tests on the builder architecture before cross-compiling.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go test ./...
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/coredns ./cmd/coredns
RUN --mount=type=cache,target=/go/pkg/mod bash scripts/licenses.sh /out/licenses

FROM build AS fixture-build
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/dns-fixture ./cmd/dns-fixture
FROM scratch AS fixture
COPY --from=fixture-build /out/dns-fixture /dns-fixture
USER 65532:65532
ENTRYPOINT ["/dns-fixture"]

FROM scratch AS runtime
COPY --from=build --chmod=0555 /out/coredns /coredns
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Create the directory with traversal permission BEFORE the read-only file copy.
WORKDIR /etc/coredns
COPY --chmod=0444 Corefile /etc/coredns/Corefile
COPY --from=build /out/licenses /licenses
ARG VERSION=1.0.0
LABEL org.opencontainers.image.title="CoreDNS IPv4-preferred resolver" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.description="Conditional AAAA filtering with IPv6-only fallback; CoreDNS 1.15.0"
ENV UPSTREAM_1="tls://1.1.1.1" UPSTREAM_2="tls://1.0.0.1" TLS_SERVER_NAME="one.one.one.one" GOMEMLIMIT=96MiB
USER 65532:65532
WORKDIR /
EXPOSE 1053/udp 1053/tcp 8080/tcp 8181/tcp 9153/tcp
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/coredns", "-healthcheck"]
ENTRYPOINT ["/coredns"]
CMD ["-conf", "/etc/coredns/Corefile"]
