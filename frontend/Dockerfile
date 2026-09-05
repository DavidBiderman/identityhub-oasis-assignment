# Build the single-page application, then serve it from nginx.
#
# The two stages keep node out of the runtime image: what ships is static files
# and a web server, with no build toolchain and no package manager.

FROM node:24-alpine AS build
WORKDIR /app

# Dependencies are copied and installed before the source, so a source change
# does not invalidate the dependency layer.
COPY package.json package-lock.json* ./
RUN npm ci

COPY . .
RUN npm run build

FROM nginx:1.27-alpine AS runtime
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY security-headers.conf /etc/nginx/security-headers.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 8080

# nginx runs as root only to bind its master process; workers drop to nginx.
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -qO- http://localhost:8080/index.html >/dev/null || exit 1
