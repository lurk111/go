FROM --platform=$BUILDPLATFORM node:22-bookworm-slim@sha256:43ac6c60b8f89723f746e8a92ce91abd5017e627ce1ddfe4238355d3a30b772c AS frontend
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80 AS test-runner
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd ./cmd
COPY examples ./examples
COPY webui ./webui
CMD ["go", "test", "-count=1", "-v", "./internal/..."]

FROM test-runner AS builder
ARG TARGETOS=linux
ARG TARGETARCH
COPY --from=frontend /src/webui/dist ./webui/dist
RUN go test ./internal/... && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /bid-mon ./cmd/bid-mon && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /helper ./cmd/evidence-helper

FROM python:3.11-slim@sha256:e41613d42d4891e4930f79523f93f81bbc7632584ec65e36ab055f41a800b41e AS ssh-target
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home --shell /bin/sh diag \
    && passwd -d diag && mkdir -p /run/sshd
COPY remote_helper/sshd_config.go /etc/ssh/sshd_config
COPY --from=builder /helper /opt/bid-mon-helper
CMD ["/bin/sh", "-c", "mkdir -p /run/sshd && exec /usr/sbin/sshd -D -e"]

FROM scratch AS runtime
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /bid-mon /bid-mon
USER 10001:10001
ENTRYPOINT ["/bid-mon"]
