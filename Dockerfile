# syntax=docker/dockerfile:1.7

FROM golang:1.26.5@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/yuanshu ./cmd/yuanshu && \
    install -d -m 0700 /out/data

FROM node:24.18.1-bookworm-slim@sha256:235600a8101ab264e117b1768e925532262668dc9b581ef1dd7d96ced463b8e7 AS codex
WORKDIR /opt/codex
COPY deploy/container/codex/package.json deploy/container/codex/package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS server
LABEL org.opencontainers.image.title="Yuanshu Server" \
      org.opencontainers.image.description="Yuanshu self-hosted relay and metadata server" \
      org.opencontainers.image.source="https://github.com/yuanshu-ai/yuanshu" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=builder --chown=1000:1000 /out/yuanshu /usr/local/bin/yuanshu
COPY --from=builder --chown=1000:1000 /out/data /data
USER 1000:1000
EXPOSE 7444
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/yuanshu", "server", "healthcheck", "--address", "127.0.0.1:7444"]
ENTRYPOINT ["/usr/local/bin/yuanshu"]
CMD ["server", "--help"]

FROM node:24.18.1-bookworm-slim@sha256:235600a8101ab264e117b1768e925532262668dc9b581ef1dd7d96ced463b8e7 AS standalone
LABEL org.opencontainers.image.title="Yuanshu Standalone" \
      org.opencontainers.image.description="Yuanshu self-hosted server and local Codex node" \
      org.opencontainers.image.source="https://github.com/yuanshu-ai/yuanshu" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.codex.version="0.144.6"
COPY --from=builder --chown=1000:1000 /out/yuanshu /usr/local/bin/yuanshu
COPY --from=builder --chown=1000:1000 /out/data /data
COPY --from=codex --chown=1000:1000 /opt/codex /opt/codex
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack /opt/yarn-v1.22.22 && \
    rm -f /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack /usr/local/bin/yarn /usr/local/bin/yarnpkg && \
    install -d -o 1000 -g 1000 -m 0700 /codex-home /workspace
ENV PATH="/opt/codex/node_modules/.bin:${PATH}" \
    CODEX_HOME=/codex-home \
    XDG_RUNTIME_DIR=/tmp/yuanshu-runtime
USER 1000:1000
EXPOSE 7444
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/yuanshu", "server", "healthcheck", "--address", "127.0.0.1:7444"]
ENTRYPOINT ["/usr/local/bin/yuanshu"]
CMD ["standalone", "--help"]
