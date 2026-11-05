# VPN Topology hub image (build context: repository root).
# Contains: the hub (NestJS), the vpnmap binary (it parses the topology files
# at bootstrap) and the agent binaries + install scripts served at /agent/.
# Iranian mirrors can be passed as build args, like the XBridger images.
ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.24-alpine

FROM ${GO_IMAGE} AS go
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg ./pkg
COPY apps/cli ./apps/cli
COPY apps/agent ./apps/agent
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vpnmap ./apps/cli \
 && mkdir -p /out/agent \
 && for arch in amd64 arm64; do \
      GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/agent/vpnmap-agent-linux-$arch ./apps/agent; \
      (cd /out/agent && sha256sum vpnmap-agent-linux-$arch | cut -d' ' -f1 > vpnmap-agent-linux-$arch.sha256); \
    done \
 && cp apps/agent/deploy/install.sh apps/agent/deploy/uninstall.sh /out/agent/ \
 && mkdir -p /out/agent/checks && cp apps/agent/deploy/checks.d/*.check /out/agent/checks/

FROM ${NODE_IMAGE} AS build
ARG NPM_REGISTRY=https://registry.npmjs.org
ARG ALPINE_MIRROR=
RUN if [ -n "$ALPINE_MIRROR" ]; then sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${ALPINE_MIRROR}#g" /etc/apk/repositories; fi
RUN npm config set registry ${NPM_REGISTRY} && npm i -g pnpm@10.28.0
WORKDIR /app
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY apps/hub/package.json apps/hub/
RUN pnpm config set registry ${NPM_REGISTRY} && pnpm install --frozen-lockfile --filter @vpnmap/hub
COPY apps/hub apps/hub
RUN pnpm --filter @vpnmap/hub build && pnpm --filter @vpnmap/hub deploy --prod --legacy /deploy

FROM ${NODE_IMAGE}
ARG ALPINE_MIRROR=
RUN if [ -n "$ALPINE_MIRROR" ]; then sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${ALPINE_MIRROR}#g" /etc/apk/repositories; fi \
 && apk add --no-cache tini
WORKDIR /app
COPY --from=build /deploy/node_modules ./node_modules
COPY --from=build /app/apps/hub/dist ./dist
# Geist fonts for the board pictures (Telegram, /api/board/image).
COPY apps/hub/assets ./assets
COPY --from=go /out/vpnmap /usr/local/bin/vpnmap
COPY --from=go /out/agent /app/agent-dist
# Topology files for the very first start (afterwards versions live in Postgres
# and change only through `vpnmap apply`).
COPY topology /app/repo/topology
ENV NODE_ENV=production PORT=4000 TOPOLOGY_ROOT=/app/repo AGENT_DIST_DIR=/app/agent-dist VPNMAP_BIN=/usr/local/bin/vpnmap
USER node
EXPOSE 4000
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:4000/api/health || exit 1
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["node", "--enable-source-maps", "dist/main.js"]
