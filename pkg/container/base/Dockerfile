FROM flanksource/base-image:latest

ARG TZ
ENV TZ="$TZ"

ARG CLAUDE_CODE_VERSION=latest
ARG USERNAME=claude
ARG USER_UID=501
ARG USER_GID=20

# Create user/group matching host (default: moshe:501:20)
RUN if ! getent group ${USER_GID} > /dev/null 2>&1; then groupadd -g ${USER_GID} ${USERNAME}; fi && \
  useradd -u ${USER_UID} -g ${USER_GID} -m -s /bin/zsh ${USERNAME} && \
  mkdir -p /home/${USERNAME}/.claude && \
  chown -R ${USERNAME}:${USER_GID} /home/${USERNAME}

# Install Node.js and basic development tools
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
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
  && apt-get clean && rm -rf /var/lib/apt/lists/*

# Ensure user has access to /usr/local/share
RUN mkdir -p /usr/local/share/npm-global && \
  chown -R ${USERNAME} /usr/local/share


# Set `DEVCONTAINER` environment variable to help with orientation
ENV DEVCONTAINER=true

# Create workspace directory
RUN mkdir -p /workspace && chown ${USERNAME}:${USER_GID} /workspace

WORKDIR /workspace

ARG GIT_DELTA_VERSION=0.18.2
RUN ARCH=$(dpkg --print-architecture) && \
  wget "https://github.com/dandavison/delta/releases/download/${GIT_DELTA_VERSION}/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
  sudo dpkg -i "git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
  rm "git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb"

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER ${USERNAME}

# Install global packages
ENV NPM_CONFIG_PREFIX=/usr/local/share/npm-global
ENV PATH=$PATH:/usr/local/share/npm-global/bin

# Set the default shell to zsh rather than sh
ENV SHELL=/bin/zsh

# Set the default editor and visual
ENV EDITOR=nano
ENV VISUAL=nano

# Install Claude
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}

USER root
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
