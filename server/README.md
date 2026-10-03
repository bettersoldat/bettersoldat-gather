# The gather server

The game half: a SoldatReloaded dedicated server with [gather.lua](gather.lua) as its
script, packaged as a Docker image for fly.io (or anywhere). Run as many as you like;
each tells the one bot its name.

## What the script does

- **Keeps the door.** It sets the server's password (`sv_password`, read live) to
  whatever the bot says: a new one as a gather starts, DMed to the players, and another
  as it ends, so the server is locked in between. A player joins with it from the main
  menu's Join page (`cl_password`).
- **Runs the series.** It keeps one request open to the bot, answered the moment the
  state changes (a long poll, re-issued as it returns), so a new gather or password
  reaches the server at once. When the bot says a gather is on, the maps are started
  from the chat, counted when they run to their end,
  `/votemap` is refused, and each counted map's end is posted to the bot. The series
  is over after two maps with a team ahead, or after the third.
- **Takes a few chat commands**, from anyone but a spectator:

| said | what |
|---|---|
| `!map <name>` | starts a CTF map of the bot's pool (`ash` finds ctf_Ash) as the series' next map; with no gather on, just changes the map |
| `!r` | replays the current map from the start; the round it cuts short counts for nothing |
| `!tb` | at 1-1 after two maps, plays the tiebreaker the bot drew |
| `!p` | pauses the game |
| `!up` | counts 3, 2, 1 and goes on |
| `!status` | the gather, the teams, the series, and the round's score and time (spectators too) |

A map started while another counted map is on cuts that one short, uncounted. After
a counted map ends, the rotation plays it again as warm-up until the next `!map`.

## Environment

The script's:

| variable | what | default |
|---|---|---|
| `GATHER_BOT_URL` | where the bot answers | `http://127.0.0.1:8080` |
| `GATHER_SECRET` | the bot's `GATHER_SECRET` | `change-me` |
| `GATHER_SERVER_NAME` | this server's name in the bot's `GATHER_SERVERS`; may stay empty when the bot has one server | empty |
| `GATHER_RETRY` | seconds before asking again when the bot is unreachable | `3` |

The server's, read by [entrypoint.sh](entrypoint.sh):

| variable | what | default |
|---|---|---|
| `SV_PORT` | the UDP port | `23073` |
| `SV_IP` | the address to listen on; empty for every one, and on fly.io the `fly-global-services` address is found by itself | empty |
| `SV_HOSTNAME` | the name on the scoreboard | `SoldatReloaded gather` |
| `SV_MAP` | the map between gathers | `ctf_Ash` |
| `SV_TIMELIMIT` | minutes a round lasts | `10` |
| `SV_KILLLIMIT` | captures that win a round | `10` |

Anything else goes on the command line as the server's own `+cvar value`.

## Without Docker

From SoldatReloaded's directory, with the script and a gather's limits:

```bash
GATHER_BOT_URL=http://127.0.0.1:8080 GATHER_SECRET=change-me ./soldatreloaded-server +sv_script ../gather-bot/server/gather.lua +sv_timelimit 10 +sv_killlimit 10
```

The time and kill limits are the server's and read as it starts, so they go on the
command line or in its `config/server.cfg`.

## The image

[Dockerfile](Dockerfile) downloads SoldatReloaded's Linux server release
(`soldatreloaded-<version>-linux-x86_64-server.zip` from
[the releases](https://github.com/soldatreloaded/soldatreloaded/releases): the executable
and the assets a server reads) and adds the script. Nothing is compiled. Releases before
v0.8.0 are `.tar.gz`, which the Dockerfile falls back to.
Releases up to v0.3.2, from before the game was renamed, are named
`bettersoldat-<version>-…` with a `bettersoldat-server` inside; the Dockerfile falls back
to that name and renames the executable, so an older tag still builds.
`SOLDATRELOADED_VERSION` is a release tag, or `latest` for the newest.

```bash
docker build -t soldatreloaded-gather --build-arg SOLDATRELOADED_VERSION=latest .
```

```bash
docker run -p 23073:23073/udp -e GATHER_BOT_URL=https://gather-bot.fly.dev -e GATHER_SECRET=... -e GATHER_SERVER_NAME=eu1 soldatreloaded-gather
```

The entrypoint sets the server's `sv_ip` cvar (`feat(server): sv_ip, the address to
listen on`, after v0.2.0): `+sv_ip <address>` binds that address alone, empty binds
every one. The build refuses a release from before it, since a server that listens on
every address goes unanswered on fly.io. The script also needs `sv_password`
(`feat(net): a server password`, which bumps the wire to version 9, so the client has
to be as new). v0.3.1 is the first release with both; fly.toml pins it.

## On fly.io

One Fly app per server. [fly.toml](fly.toml) is one server's; for another, copy it
(`fly.na1.toml`), change `app`, `primary_region`, `SV_HOSTNAME` and
`GATHER_SERVER_NAME`, and pass `--config fly.na1.toml` to every command below.

### UDP on Fly, and what the files do about it

- **Bind the `fly-global-services` address.** Fly's proxy rewrites the addresses of UDP
  packets; an app listening on every address answers from its private address and the
  answers never reach the player. The entrypoint resolves `fly-global-services` and
  starts the server with `+sv_ip` on it.
- **Same port inside and out.** Fly rewrites addresses, not ports, so `internal_port`
  and `services.ports.port` are both 23073.
- **A dedicated IPv4.** UDP doesn't work over Fly's shared IPv4 or public IPv6, so the
  app needs its own IPv4 (`fly ips allocate-v4`, a couple of dollars a month).
  SoldatReloaded's wire is IPv4 anyway.
- **Keep the machine running.** The proxy can't wake a stopped machine for a UDP
  packet, so `auto_stop_machines = "off"` and `min_machines_running = 1`.
- **Packet size.** WireGuard and the UDP proxy take about 72 bytes of each packet;
  SoldatReloaded's packets are at most 1200 bytes (`NET_MTU`), under the limit.

### The first deploy

From this directory, with [flyctl](https://fly.io/docs/flyctl/install/) installed and
`fly auth login` done:

```bash
fly launch --no-deploy --copy-config --name soldatreloaded-eu1
```

```bash
fly ips allocate-v4
```

```bash
fly secrets set GATHER_SECRET=...
```

```bash
fly deploy --ha=false
```

`--ha=false` keeps it to one machine; Fly otherwise adds a second "for high
availability", which here would be a second game server behind the same address,
with players landing on either. `fly scale count 1` puts it right if that happened.

Then:

```bash
fly ips list
```

gives the IPv4 players connect to, as `<ip>:23073`; the app's name also resolves to
it, so `soldatreloaded-eu1.fly.dev:23073` works in the client and is what goes into the
bot's `GATHER_SERVERS` (`eu1=soldatreloaded-eu1.fly.dev:23073`).

### Day to day

```bash
fly logs
```

```bash
fly status
```

```bash
fly deploy --build-arg SOLDATRELOADED_VERSION=v0.3.0   # a particular SoldatReloaded release; "latest" otherwise
```

```bash
fly scale count 1 --region ams    # it should only ever be one machine per app
```

```bash
fly ssh console
```

The server's console reads its standard input, which a Fly machine doesn't have, so
anything the console would be typed is said in the game instead (`!map`, `!p`, `!r`)
or set in the environment and redeployed (`fly secrets set` for the secret,
`fly deploy -e SV_TIMELIMIT=15` or an edit to fly.toml for the rest).
