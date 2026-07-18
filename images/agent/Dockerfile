# syntax=docker/dockerfile:1.7
ARG NODE_IMAGE=node:24.14.0-trixie-slim@sha256:8c8f12cedb96c3b59642cf30d713943c2b223990c9919b96a141681f62e6e292

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
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 bosun \
    && useradd --uid 10001 --gid bosun --create-home --shell /bin/bash bosun

COPY --from=cli /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=cli /usr/local/bin/claude /usr/local/bin/claude
COPY --chmod=0755 entrypoint.sh /usr/local/bin/bosun-entrypoint

ENV CLAUDE_CODE_VERSION=${CLAUDE_CODE_VERSION} \
    HOME=/home/bosun \
    TERM=xterm-256color
USER 10001:10001
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/bosun-entrypoint"]
