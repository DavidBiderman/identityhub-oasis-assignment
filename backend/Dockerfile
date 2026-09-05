# One build, four binaries.
#
# Compiling once and copying each binary into its own minimal runtime image
# keeps the dependency download and the type-check shared across all four, and
# keeps what ships free of a toolchain, a shell and a package manager.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first, so that a source change does not invalidate the module
# download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off produces a static binary, which is what lets the runtime images be
# distroless. Symbol tables are stripped because nothing debugs from them here.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/api       ./cmd/api      && \
    go build -trimpath -ldflags="-s -w" -o /out/worker    ./cmd/worker   && \
    go build -trimpath -ldflags="-s -w" -o /out/migrate   ./cmd/migrate  && \
    go build -trimpath -ldflags="-s -w" -o /out/bootstrap ./cmd/bootstrap

# The runtime base carries CA certificates -- needed to reach Atlassian and the
# blog over TLS -- a nonroot user, and nothing else.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
USER nonroot:nonroot
WORKDIR /app

FROM runtime AS api
COPY --from=build /out/api /app/api
EXPOSE 8080
ENTRYPOINT ["/app/api"]

FROM runtime AS worker
COPY --from=build /out/worker /app/worker
ENTRYPOINT ["/app/worker"]

FROM runtime AS migrate
COPY --from=build /out/migrate /app/migrate
ENTRYPOINT ["/app/migrate"]

FROM runtime AS bootstrap
COPY --from=build /out/bootstrap /app/bootstrap
ENTRYPOINT ["/app/bootstrap"]
