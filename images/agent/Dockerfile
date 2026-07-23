# syntax=docker/dockerfile:1.7
ARG NODE_IMAGE=node:24.14.0-trixie-slim@sha256:8c8f12cedb96c3b59642cf30d713943c2b223990c9919b96a141681f62e6e292
ARG GO_IMAGE=golang:1.26.2-alpine3.23@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1

FROM ${GO_IMAGE} AS auth-proxy
WORKDIR /src
COPY go.mod ./
COPY cmd/auth-proxy ./cmd/auth-proxy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bosun-auth-proxy ./cmd/auth-proxy

FROM ${NODE_IMAGE} AS cli
ARG CLAUDE_CODE_VERSION=2.1.214
RUN npm install --global "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
    && test "$(claude --version | awk '{print $1}')" = "${CLAUDE_CODE_VERSION}"

FROM ${NODE_IMAGE}
ARG CLAUDE_CODE_VERSION=2.1.214
RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        openssh-client \
        procps \
        ripgrep \
        tmux \
        nano \
        python3 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 bosun \
    && useradd --uid 10001 --gid bosun --create-home --shell /bin/bash bosun \
    && install -d -m 0555 /etc/claude-code /usr/local/lib/bosun/hooks /usr/local/share/bosun

COPY --from=cli /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=cli /usr/local/bin/claude /usr/local/bin/claude
COPY --from=auth-proxy /out/bosun-auth-proxy /usr/local/bin/bosun-auth-proxy
COPY --chmod=0755 entrypoint.sh /usr/local/bin/bosun-entrypoint
COPY --chmod=0755 shell-supervisor.sh /usr/local/bin/bosun-shell-supervisor
COPY --chmod=0755 runtime-control.sh /usr/local/bin/bosun-runtime-control
COPY --chmod=0755 hooks/session-start.sh /usr/local/lib/bosun/hooks/session-start
COPY --chmod=0755 hooks/agent-state.sh /usr/local/lib/bosun/hooks/agent-state
COPY --chmod=0444 hooks/managed-settings.json /etc/claude-code/managed-settings.json
COPY --chmod=0444 recovery.schema.json /usr/local/share/bosun/recovery.schema.json

ENV CLAUDE_CODE_VERSION=${CLAUDE_CODE_VERSION} \
    DISABLE_AUTOUPDATER=1 \
    HOME=/workspace/.bosun-home \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    TERM=xterm-256color \
    TMUX_TMPDIR=/run/bosun-tmux
USER 10001:10001
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/bosun-entrypoint"]
