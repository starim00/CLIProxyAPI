# syntax=docker/dockerfile:1
# RUNTIME_IMAGE must be the retained production image, including its libraries.
ARG RUNTIME_IMAGE
FROM golang:1.26-bookworm AS source
WORKDIR /src
ENV GOMAXPROCS=2 GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Explicit validation target; never a dependency of the production image.
# Build --target validation --build-arg TEST_SCOPE=config once for config changes.
FROM source AS validation
ARG TEST_SCOPE=custom
RUN --network=default --mount=type=cache,target=/root/.cache/go-build \
    sh scripts/verify-release.sh "$TEST_SCOPE"

FROM source AS builder
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -p 2 -buildvcs=false \
    -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
    -o /out/CLIProxyAPI ./cmd/server

FROM ${RUNTIME_IMAGE} AS runtime
COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY config.example.yaml /CLIProxyAPI/config.example.yaml
ARG COMMIT
LABEL org.opencontainers.image.source="https://github.com/starim00/CLIProxyAPI" \
      org.opencontainers.image.revision="${COMMIT}"
