# syntax=docker/dockerfile:1
FROM golang:1.26 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto GOWORK=off

COPY pkg/ pkg/
COPY gen/ gen/
COPY services/seller/ services/seller/

WORKDIR /src/services/seller
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/seller .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/seller /seller
USER 65532:65532
EXPOSE 50051
ENTRYPOINT ["/seller"]
