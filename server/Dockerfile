# A bettersoldat gather server: the headless server built from the bettersoldat
# repository at BETTERSOLDAT_REF, the assets a server reads (no art, no sound), and
# gather.lua as its script. Build from this directory:
#   docker build -t bettersoldat-gather .
#   docker run -p 23073:23073/udp -e GATHER_BOT_URL=... -e GATHER_SECRET=... bettersoldat-gather
#
# The build needs SDL2's development headers even though the server never uses it:
# xmake installs every package the project requires as it configures.
ARG BETTERSOLDAT_REPO=https://github.com/bettersoldat/bettersoldat
ARG BETTERSOLDAT_REF=main

FROM ubuntu:24.04 AS build
ARG BETTERSOLDAT_REPO
ARG BETTERSOLDAT_REF
ENV DEBIAN_FRONTEND=noninteractive XMAKE_ROOT=y
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake pkg-config git curl ca-certificates unzip \
      libx11-dev libxext-dev libxrandr-dev libxcursor-dev libxi-dev libxinerama-dev \
      libxss-dev libxfixes-dev libxrender-dev libxkbcommon-dev \
      libwayland-dev wayland-protocols libdecor-0-dev libegl1-mesa-dev libgl1-mesa-dev \
      libasound2-dev libpulse-dev libdbus-1-dev libudev-dev libdrm-dev libgbm-dev \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://xmake.io/shget.text | bash
ENV PATH="/root/.local/bin:${PATH}"

WORKDIR /src
RUN git clone "${BETTERSOLDAT_REPO}" . && git checkout "${BETTERSOLDAT_REF}"

# fly.io's UDP needs the server to bind one address (README.md), which sv_ip does; a
# ref from before it would listen on every one and go unanswered there, so the build
# stops on such a ref rather than make a server that fails quietly.
RUN grep -q '"sv_ip"' server/main.c \
    || { echo "bettersoldat ${BETTERSOLDAT_REF} has no sv_ip: use a ref with feat(server): sv_ip" >&2; exit 1; }

RUN xmake f -m release -y && xmake build -y server

# what the server's package holds (xmake.lua, server_needs): the executable, the
# config, and the assets but the art and the sound
RUN mkdir -p /out/assets \
    && cp build/linux/x86_64/release/bettersoldat-server /out/ \
    && cp config.cfg /out/ \
    && for entry in assets/*; do \
         name=$(basename "$entry"); \
         case "$name" in \
           *-gfx|textures|custom-interfaces|sfx|icon.bmp|play-regular.ttf|OFL.txt|mod.ini) ;; \
           *) cp -r "$entry" /out/assets/ ;; \
         esac; \
       done

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --create-home --home-dir /app soldat
WORKDIR /app
COPY --from=build --chown=soldat:soldat /out/ /app/
COPY --chown=soldat:soldat gather.lua /app/scripts/server.lua
COPY --chown=soldat:soldat entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
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
