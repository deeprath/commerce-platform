# syntax=docker/dockerfile:1
# Built without the go.work workspace (GOWORK=off): the service's own go.mod
# replace directive resolves pkg/ from the copied tree.
FROM golang:1.26 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto GOWORK=off

COPY pkg/ pkg/
COPY services/ext-authz/ services/ext-authz/

WORKDIR /src/services/ext-authz
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/ext-authz .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ext-authz /ext-authz
USER 65532:65532
EXPOSE 50051
ENTRYPOINT ["/ext-authz"]
