# A bettersoldat gather server: the headless server as bettersoldat releases it for
# Linux (the executable, config.cfg and the assets a server reads), with gather.lua as
# its script. Nothing is compiled. Build from this directory:
#   docker build -t bettersoldat-gather .
#   docker run -p 23073:23073/udp -e GATHER_BOT_URL=... -e GATHER_SECRET=... bettersoldat-gather
#
# BETTERSOLDAT_VERSION is a release tag (v0.3.0) or "latest".
ARG BETTERSOLDAT_REPO=bettersoldat/bettersoldat
ARG BETTERSOLDAT_VERSION=latest

FROM ubuntu:24.04 AS fetch
ARG BETTERSOLDAT_REPO
ARG BETTERSOLDAT_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /fetch
# "latest" is whatever github.com/.../releases/latest redirects to; the asset is named
# after the version without its v
RUN set -eu; \
    tag="$BETTERSOLDAT_VERSION"; \
    if [ "$tag" = "latest" ]; then \
      tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${BETTERSOLDAT_REPO}/releases/latest" | sed 's#.*/tag/##'); \
    fi; \
    version="${tag#v}"; \
    name="bettersoldat-${version}-linux-x86_64-server"; \
    echo "fetching ${name} (${tag})"; \
    curl -fsSL -o server.tar.gz "https://github.com/${BETTERSOLDAT_REPO}/releases/download/${tag}/${name}.tar.gz"; \
    tar -xzf server.tar.gz; \
    mv "${name}" /out; \
    ls -la /out
# fly.io's UDP needs the server to bind one address (README.md), which sv_ip does; a
# release from before it would listen on every one and go unanswered there, so the
# build stops on such a release rather than make a server that fails quietly.
RUN grep -q sv_ip /out/bettersoldat-server \
    || { echo "this bettersoldat release has no sv_ip: use one from after feat(server): sv_ip" >&2; exit 1; }

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --create-home --home-dir /app soldat
WORKDIR /app
COPY --from=fetch --chown=soldat:soldat /out/ /app/
COPY --chown=soldat:soldat gather.lua /app/scripts/server.lua
COPY --chown=soldat:soldat entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh /app/bettersoldat-server
USER soldat

# the server's own settings; the GATHER_* ones are the script's (gather.lua)
ENV SV_PORT=23073 \
    SV_HOSTNAME="bettersoldat gather" \
    SV_MAP=ctf_Ash \
    SV_TIMELIMIT=10 \
    SV_KILLLIMIT=10 \
    GATHER_BOT_URL=http://127.0.0.1:8080 \
    GATHER_SERVER_NAME= \
    GATHER_GRACE=30
EXPOSE 23073/udp
ENTRYPOINT ["/app/entrypoint.sh"]
