# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
COPY web/admin/package.json web/admin/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/admin/ ./
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.27-alpine
# Non-root by default in this image (uid 101). Serves on :8080.
COPY --from=build /app/dist /usr/share/nginx/html
COPY web/admin/nginx.conf.template /etc/nginx/templates/default.conf.template
ENV BFF_UPSTREAM=http://bff:8080 \
    MEDIA_ORIGIN=http://localhost:9000
EXPOSE 8080
