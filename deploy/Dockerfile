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
# Python is needed only to run the tests of programs with extern python blocks.
RUN apk add --no-cache python3
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

# App image for programs with extern python blocks: the Pygo executable plus
# a Python runtime. Python packages listed in requirements.txt (next to the
# program, or at the repository root) are installed.
#   docker build -f deploy/Dockerfile --target app-python --build-arg APP=my.pg --build-arg ALLOW=python,net -t my-app .
FROM python:3.12-slim AS app-python
ARG APP=examples/python_stats.pg
COPY --from=bundle /app-src /tmp/src
RUN set -e; req="$(dirname /tmp/src/$APP)/requirements.txt"; \
    [ -f "$req" ] || req=/tmp/src/requirements.txt; \
    if [ -f "$req" ]; then pip install --no-cache-dir -r "$req"; fi; \
    rm -rf /tmp/src
COPY --from=bundle /out/app /app
RUN useradd --system --uid 65532 pygo
USER 65532
ENV PORT=8080 PYGO_PYTHON=python3
ENTRYPOINT ["/app"]

# App image (default target): one static self-contained executable, non-root, no shell.
FROM gcr.io/distroless/static-debian12:nonroot AS app
COPY --from=bundle /out/app /app
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app"]
