# syntax=docker/dockerfile:1
# Build context is the repo root. Only the workspace modules this service needs
# are copied (see .dockerignore).

FROM golang:1.26 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto

COPY go.work go.work.sum* ./
COPY pkg/ pkg/
COPY gen/ gen/
COPY services/ext-authz/ services/ext-authz/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/ext-authz ./services/ext-authz

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ext-authz /ext-authz
USER 65532:65532
EXPOSE 50051
ENTRYPOINT ["/ext-authz"]
