# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS openflux-build
ARG OPENFLUX_UPSTREAM_REPO=https://github.com/p1neappleXpress/OpenFlux.git
RUN apk add --no-cache git
WORKDIR /src
RUN git clone --depth 1 "$OPENFLUX_UPSTREAM_REPO" .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/openflux . && git rev-parse HEAD >/out/upstream-version

FROM golang:1.26-alpine AS panel-build
WORKDIR /src
COPY source/deploy/panel/ ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/openflux-panel .

FROM golang:1.26-alpine
RUN apk add --no-cache bash ca-certificates curl git iptables jq openssl
COPY --from=openflux-build /out/openflux /usr/local/lib/openflux-deploy/openflux.seed
COPY --from=openflux-build /out/upstream-version /usr/local/lib/openflux-deploy/upstream-version.seed
COPY --from=panel-build /out/openflux-panel /usr/local/bin/openflux-panel
COPY update.sh /usr/local/lib/openflux-deploy/update.sh
COPY entrypoint.sh /usr/local/bin/openflux-entrypoint
RUN chmod 755 /usr/local/bin/openflux-panel /usr/local/bin/openflux-entrypoint /usr/local/lib/openflux-deploy/update.sh
ENTRYPOINT ["/usr/local/bin/openflux-entrypoint"]
