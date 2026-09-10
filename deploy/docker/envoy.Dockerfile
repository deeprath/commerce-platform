# syntax=docker/dockerfile:1
# Edge Envoy with the Coraza (OWASP CRS) proxy-wasm module baked in.
#
# The .wasm is fetched from a pinned GitHub release and checksum-verified at
# build time — no ~18 MB binary in git, no fragile fetch at container start.
# The Envoy config stays a bind mount (deploy/compose/docker-compose.yml) so it
# can be iterated without a rebuild.
ARG ENVOY_TAG=v1.32-latest

FROM alpine:3.21 AS fetch
ARG CORAZA_WASM_VERSION=0.6.0
ARG CORAZA_WASM_SHA256=cca4e3c75cf6b2e615907f936a1b6dcd0955250e0fb7d3b1c2ecef807d84603c
RUN apk add --no-cache curl unzip
WORKDIR /tmp
RUN curl -sfL --retry 3 --retry-delay 2 -o coraza.zip \
      "https://github.com/corazawaf/coraza-proxy-wasm/releases/download/${CORAZA_WASM_VERSION}/coraza-proxy-wasm-${CORAZA_WASM_VERSION}.zip" \
 && echo "${CORAZA_WASM_SHA256}  coraza.zip" | sha256sum -c - \
 && unzip -o coraza.zip coraza-proxy-wasm.wasm

FROM envoyproxy/envoy:${ENVOY_TAG}
COPY --from=fetch /tmp/coraza-proxy-wasm.wasm /etc/envoy/coraza-proxy-wasm.wasm
