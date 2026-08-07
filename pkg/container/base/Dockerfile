# syntax=docker/dockerfile:1
FROM flanksource/base-image:latest

ARG TZ
ENV TZ="$TZ"

ARG USERNAME=claude
ARG USER_UID=501
ARG USER_GID=20

ARG NODE_MAJOR=22
ARG GO_VERSION=1.26.1
ARG GIT_DELTA_VERSION=0.18.2
ARG CLAUDE_CODE_VERSION=latest
ARG CODEX_VERSION=latest
ARG GEMINI_CLI_VERSION=latest

# Create user/group matching host (default: moshe:501:20)
RUN if ! getent group ${USER_GID} > /dev/null 2>&1; then groupadd -g ${USER_GID} ${USERNAME}; fi && \
  useradd -u ${USER_UID} -g ${USER_GID} -m -s /bin/zsh ${USERNAME} && \
  mkdir -p /home/${USERNAME}/.claude && \
  chown -R ${USERNAME}:${USER_GID} /home/${USERNAME}

# Install Node.js and basic development tools
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - && \
  apt-get update && apt-get install -y --no-install-recommends \
  nodejs \
  less \
  git \
  procps \
  sudo \
  fzf \
  zsh \
  man-db \
  unzip \
  gnupg2 \
  gh \
  iptables \
  ipset \
  iproute2 \
  dnsutils \
  aggregate \
  jq \
  nano \
  vim \
  build-essential \
  gosu \
  ripgrep \
  fd-find \
  bat \
  tree \
  htop \
  lsof \
  sqlite3 \
  postgresql-client \
  openssh-client \
  rsync \
  tmux \
  shellcheck \
  && apt-get clean && rm -rf /var/lib/apt/lists/*

# Debian renames these binaries to avoid clashes; agents expect the upstream names
RUN ln -sf "$(command -v fdfind)" /usr/local/bin/fd && \
  ln -sf "$(command -v batcat)" /usr/local/bin/bat

# Ensure user has access to /usr/local/share
RUN mkdir -p /usr/local/share/npm-global && \
  chown -R ${USERNAME} /usr/local/share


# Set `DEVCONTAINER` environment variable to help with orientation
ENV DEVCONTAINER=true

# Create workspace directory
RUN mkdir -p /workspace && chown ${USERNAME}:${USER_GID} /workspace

WORKDIR /workspace

RUN ARCH=$(dpkg --print-architecture) && \
  wget "https://github.com/dandavison/delta/releases/download/${GIT_DELTA_VERSION}/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
  sudo dpkg -i "git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
  rm "git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb"

# Go toolchain — needed to build/test the Go repos agents work in
RUN ARCH=$(dpkg --print-architecture) && \
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -C /usr/local -xz
ENV GOROOT=/usr/local/go
ENV GOPATH=/home/${USERNAME}/go
ENV PATH=$PATH:/usr/local/go/bin:/home/${USERNAME}/go/bin
RUN GOBIN=/usr/local/bin go install github.com/onsi/ginkgo/v2/ginkgo@latest && \
  rm -rf /root/.cache/go-build /root/go && \
  mkdir -p ${GOPATH}/bin && chown -R ${USER_UID}:${USER_GID} ${GOPATH}

# Flanksource + Go tooling via deps (already present in the base image).
# GITHUB_TOKEN arrives as a BuildKit secret so it never lands in image history.
COPY deps.yaml /tmp/deps/deps.yaml
RUN --mount=type=secret,id=GITHUB_TOKEN,env=GITHUB_TOKEN,required=false \
  cd /tmp/deps && \
  deps --no-progress install -c deps.yaml --bin-dir /usr/bin --app-dir /opt && \
  rm -rf /tmp/deps /root/.deps

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Browser automation. agent-browser probes the Playwright browser cache, so one
# world-readable Chromium under PLAYWRIGHT_BROWSERS_PATH serves every user in the
# container — no per-home copy. Playwright is also what installs the shared
# libraries Chromium links against; `agent-browser install --with-deps` cannot,
# because it still asks for noble's pre-t64 package names.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN npx -y playwright install --with-deps chromium && \
  chmod -R a+rX /ms-playwright && \
  apt-get clean && rm -rf /var/lib/apt/lists/* /root/.npm

USER ${USERNAME}

# Install global packages
ENV NPM_CONFIG_PREFIX=/usr/local/share/npm-global
ENV PATH=$PATH:/usr/local/share/npm-global/bin

# Set the default shell to zsh rather than sh
ENV SHELL=/bin/zsh

# Set the default editor and visual
ENV EDITOR=nano
ENV VISUAL=nano

# Agent CLIs: one per captain backend (claude-cli, codex-cli/codex-agent, gemini-cli),
# plus tsx for the claude-agent SDK bridge and pnpm for `captain serve --dev`.
RUN npm install -g \
  @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
  @openai/codex@${CODEX_VERSION} \
  @google/gemini-cli@${GEMINI_CLI_VERSION} \
  tsx \
  typescript \
  pnpm \
  agent-browser \
  && npm cache clean --force

USER root
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
