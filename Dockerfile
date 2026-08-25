FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=0.1.0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hapudding ./cmd/hapudding \
 && ln -s hapudding /out/hap

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      bash curl vim-tiny iproute2 iputils-ping netcat-openbsd dnsutils ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -l --system --uid 65532 --create-home --home-dir /home/hap --shell /usr/sbin/nologin hap
COPY --from=build /out/hapudding /usr/local/bin/hapudding
COPY --from=build /out/hap /usr/local/bin/hap
EXPOSE 8096 9100
USER hap
ENTRYPOINT ["hapudding"]
