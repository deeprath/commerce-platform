# syntax=docker/dockerfile:1
# Built without the go.work workspace (GOWORK=off): the service's own go.mod
# replace directives resolve pkg/ and gen/go/ from the copied tree, so the image
# doesn't need every other module present.
FROM golang:1.26 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto GOWORK=off

COPY pkg/ pkg/
COPY gen/ gen/
COPY services/catalog/ services/catalog/

WORKDIR /src/services/catalog
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/catalog .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/catalog /catalog
USER 65532:65532
EXPOSE 50051
ENTRYPOINT ["/catalog"]
