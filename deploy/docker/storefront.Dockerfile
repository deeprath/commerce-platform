# syntax=docker/dockerfile:1
FROM node:26-alpine AS build
WORKDIR /app
COPY web/storefront/package.json web/storefront/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/storefront/ ./
ARG VITE_MEDIA_BASE_URL=http://localhost:9000
ENV VITE_MEDIA_BASE_URL=$VITE_MEDIA_BASE_URL
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.31-alpine
# Non-root by default in this image (uid 101) — but Trivy's Dockerfile
# scanner (DS-0002) only sees a USER instruction it can read directly in
# this file, not one inherited from the base image, so it's stated
# explicitly here too: no weaker than what the base already does, just
# visible to the scanner and pinned against a future base-image change.
USER 101
COPY --from=build /app/dist /usr/share/nginx/html
COPY web/storefront/nginx.conf.template /etc/nginx/templates/default.conf.template
ENV BFF_UPSTREAM=http://bff:8080 \
    MEDIA_ORIGIN=http://localhost:9000
EXPOSE 8080
