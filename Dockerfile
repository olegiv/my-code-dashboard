# Optional container image for a trusted single-user host. See docs/docker.md.
# Keep the Go selection consistent with go.mod, Makefile, workflows and the release script.
FROM --platform=$BUILDPLATFORM golang:1.26.10-trixie@sha256:909771fef53e3dcb0950255f0c9a8b4728c3b841e61509c7b29b098de7ee02d4 AS build
ENV GOTOOLCHAIN=go1.26.10 CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
# Like the release archives, build only files named in the reviewed public manifest:
# unlisted local files under cmd/ or internal/ could otherwise be embedded and served.
COPY . /context
RUN cd /context \
    && grep -E '^(cmd|internal|scripts/notices)/|^(LICENSE|THIRD_PARTY_NOTICES\.md)$' scripts/release-files.txt \
      | xargs cp --parents -t /src --
ARG VERSION=0.1.0-dev
ARG TARGETOS
ARG TARGETARCH
RUN GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=$VERSION" -o /out/my-code-dashboard ./cmd/my-code-dashboard \
    && go run ./scripts/notices -binary /out/my-code-dashboard > /out/THIRD_PARTY_LICENSES.txt \
    && install -d -m 0700 /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
LABEL org.opencontainers.image.title="My Code Dashboard" \
      org.opencontainers.image.source="https://github.com/olegiv/my-code-dashboard" \
      org.opencontainers.image.licenses="GPL-3.0-only"
COPY --from=build /out/my-code-dashboard /usr/local/bin/my-code-dashboard
COPY --from=build /src/LICENSE /src/THIRD_PARTY_NOTICES.md /out/THIRD_PARTY_LICENSES.txt /usr/share/doc/my-code-dashboard/
# A new named volume mounted here inherits this private, nonroot-owned directory.
COPY --from=build --chown=65532:65532 --chmod=0700 /out/data /data
USER 65532:65532
# No EXPOSE: the app listens only on loopback, so run it with host networking
# instead of publishing a port.
ENTRYPOINT ["/usr/local/bin/my-code-dashboard"]
CMD ["serve", "--data-dir", "/data"]
