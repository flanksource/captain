# syntax=docker/dockerfile:1
# Pinned by digest, not by tag: republishing the same image tag has to produce
# the same contents or a rollback lands on something other than what it rolled
# back to. Bump the digest deliberately (docker buildx imagetools inspect
# flanksource/base-image:latest) rather than letting the tag drift underneath.
FROM flanksource/base-image:latest@sha256:f5f0945741678d7702eb13da393102dbdd10a19fb20b6e06a7efe241d5395173

ARG TZ
ENV TZ="$TZ"

ARG USERNAME=claude
ARG USER_UID=501
ARG USER_GID=20

# Every mutable input is an explicit default here and an override in
# .github/workflows/publish-image.yml, so a published tag records exactly what
# went into it. `latest` is deliberately absent: it is what made two builds of
# one tag differ.
ARG NODE_MAJOR=22
ARG GO_VERSION=1.26.1
# Official checksums from https://go.dev/dl/?mode=json — the archive is verified
# before extraction, so a compromised or truncated download fails the build
# instead of being unpacked into /usr/local.
ARG GO_SHA256_AMD64=031f088e5d955bab8657ede27ad4e3bc5b7c1ba281f05f245bcc304f327c987a
ARG GO_SHA256_ARM64=a290581cfe4fe28ddd737dde3095f3dbeb7f2e4065cab4eae44dfc53b760c2f7
ARG GIT_DELTA_VERSION=0.18.2
# Kept in step with go.mod's github.com/onsi/ginkgo/v2 so the container's runner
# matches the one the suites are compiled against.
ARG GINKGO_VERSION=v2.28.1
ARG PLAYWRIGHT_VERSION=1.62.1
ARG CLAUDE_CODE_VERSION=2.1.227
ARG CODEX_VERSION=0.147.0
ARG GEMINI_CLI_VERSION=0.54.4
ARG TSX_VERSION=4.23.12
ARG TYPESCRIPT_VERSION=7.0.2
ARG PNPM_VERSION=11.21.0
ARG AGENT_BROWSER_VERSION=0.34.0

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

# Go toolchain — needed to build/test the Go repos agents work in. Downloaded to
# a file and checksummed before extraction: piping curl straight into tar unpacks
# whatever arrives, so a tampered or truncated archive would land in /usr/local
# with the build still reporting success.
RUN ARCH=$(dpkg --print-architecture) && \
  case "$ARCH" in \
    amd64) GO_SHA256="${GO_SHA256_AMD64}" ;; \
    arm64) GO_SHA256="${GO_SHA256_ARM64}" ;; \
    *) echo "no pinned go${GO_VERSION} checksum for architecture ${ARCH}" >&2; exit 1 ;; \
  esac && \
  curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" && \
  echo "${GO_SHA256}  /tmp/go.tar.gz" | sha256sum -c - && \
  tar -C /usr/local -xzf /tmp/go.tar.gz && \
  rm /tmp/go.tar.gz
ENV GOROOT=/usr/local/go
ENV GOPATH=/home/${USERNAME}/go
ENV PATH=$PATH:/usr/local/go/bin:/home/${USERNAME}/go/bin
RUN GOBIN=/usr/local/bin go install github.com/onsi/ginkgo/v2/ginkgo@${GINKGO_VERSION} && \
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
RUN npx -y playwright@${PLAYWRIGHT_VERSION} install --with-deps chromium && \
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
  tsx@${TSX_VERSION} \
  typescript@${TYPESCRIPT_VERSION} \
  pnpm@${PNPM_VERSION} \
  agent-browser@${AGENT_BROWSER_VERSION} \
  && npm cache clean --force

USER root
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
