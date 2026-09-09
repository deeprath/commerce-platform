# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
COPY web/storefront/package.json web/storefront/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/storefront/ ./
ARG VITE_MEDIA_BASE_URL=http://localhost:9000
ENV VITE_MEDIA_BASE_URL=$VITE_MEDIA_BASE_URL
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.27-alpine
# Non-root by default in this image (uid 101). Serves on :8080.
COPY --from=build /app/dist /usr/share/nginx/html
COPY web/storefront/nginx.conf.template /etc/nginx/templates/default.conf.template
ENV BFF_UPSTREAM=http://bff:8080 \
    MEDIA_ORIGIN=http://localhost:9000
EXPOSE 8080
