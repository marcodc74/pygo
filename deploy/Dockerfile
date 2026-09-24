# Multi-stage build.
#   docker build -f deploy/Dockerfile -t pygo-app .                        -> app image (default: examples/server.pg)
#   docker build -f deploy/Dockerfile --build-arg APP=path/app.pg --build-arg ALLOW=net,env -t my-app .
#   docker build -f deploy/Dockerfile --target toolchain -t pygo .          -> image with the pygo CLI
ARG GO_VERSION=1.24

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/pygo ./cmd/pygo

# The program is checked and its tests run at build time: a broken program never becomes an image.
FROM build AS bundle
ARG APP=examples/server.pg
ARG ALLOW=net,env
COPY . /app-src
WORKDIR /app-src
RUN /out/pygo check "$APP" && /out/pygo test "$APP" \
 && /out/pygo build --allow "$ALLOW" -o /out/app "$APP"

# Toolchain image: pygo run/check/test/... inside containers (CI, sandboxes for agents).
FROM gcr.io/distroless/static-debian12:nonroot AS toolchain
COPY --from=build /out/pygo /usr/local/bin/pygo
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/pygo"]

# App image (default target): one static self-contained executable, non-root, no shell.
FROM gcr.io/distroless/static-debian12:nonroot AS app
COPY --from=bundle /out/app /app
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app"]
