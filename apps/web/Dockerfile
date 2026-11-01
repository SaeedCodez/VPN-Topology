# VPN Topology board: static SPA built with Nuxt, served by nginx (build context: repo root).
ARG NODE_IMAGE=node:22-alpine
FROM ${NODE_IMAGE} AS build
ARG NPM_REGISTRY=https://registry.npmjs.org
RUN npm config set registry ${NPM_REGISTRY} && npm i -g pnpm@10.28.0
WORKDIR /app
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY apps/web/package.json apps/web/
COPY apps/hub/src/contract.ts apps/hub/src/contract.ts
RUN pnpm config set registry ${NPM_REGISTRY} && pnpm install --frozen-lockfile --filter @vpnmap/web
COPY apps/web apps/web
RUN pnpm --filter @vpnmap/web build

FROM nginx:1.27-alpine
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/apps/web/.output/public /usr/share/nginx/html
EXPOSE 8080
