ARG NODE_IMAGE=node:24.14.0-alpine3.23@sha256:7fddd9ddeae8196abf4a3ef2de34e11f7b1a722119f91f28ddf1e99dcafdf114
ARG NGINX_IMAGE=nginx:1.29.5-alpine3.23@sha256:1eff5a5f3fcf8431a0abb7eddf5471fec24e5e1905a2581aeacdb07a4479b92b

FROM ${NODE_IMAGE} AS builder
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --include=optional \
    && node --input-type=module -e "await import('rolldown')"
COPY . .
RUN npm run build

FROM ${NGINX_IMAGE}
COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=builder /src/dist /usr/share/nginx/html
USER 101:101
