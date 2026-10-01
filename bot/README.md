# gatherbot

The Discord half: one bot, in one channel, running gathers on one or more SoldatReloaded
servers. 3v3 CTF, best of three: the teams start their maps in the game, the bot draws
the tiebreaker, and it is played only when the first two are split 1-1. Nothing is
ranked or persisted; restarting the bot empties the queue.

## How a gather goes

1. Players `!beta_add` in the gather channel. `!beta_del` leaves, `!beta_status` shows
   the queue and every server.
2. At six, the bot takes the first free server, shuffles the six into **Alpha** and
   **Bravo**, makes a password for the players and another for spectators, draws the
   tiebreaker from the pool, and DMs each player the server's address, their password
   and their team. With every server busy the six wait, and start on the first server
   that frees up.
3. In the game, `!map <map>` starts a map (`!beta_maps` lists the pool). A map counts
   once it runs to its end; `!r` replays it from the start and `!map` another cuts it
   short, and neither counts. At 1-1, `!tb` plays the tiebreaker. The server's script
   posts each counted map's end, and the bot shows the score, the series and
   everyone's kills, deaths, caps and ping.
4. After two maps with a team ahead, or after the third, the bot changes both
   passwords again, so the server is locked, and the queue goes on. `!beta_abort` does
   the same at any point (a player of the gather ends their own; anyone with Manage
   Server ends any, by server name).

`!beta_spec [server]` DMs a server's address and password, to watch from the
spectators. `!beta_info` sends a player their DM again. `!beta_help` lists the commands.

The password is the server's own (`sv_password`, which the script on it keeps set to
the bot's word); the players put it in the main menu's Join page with the address.

## Setting the bot up in Discord

1. Go to the [Discord developer portal](https://discord.com/developers/applications),
   **New Application**, name it.
2. **Bot** tab: **Reset Token** and copy it; that is `GATHER_DISCORD_TOKEN`. Under
   **Privileged Gateway Intents** turn on **Message Content Intent** (the bot reads the
   `!beta_` lines). Turn off **Public Bot** unless you want others to invite it.
3. **OAuth2** tab, **URL Generator**: scope `bot`; permissions **View Channels**,
   **Send Messages**, **Read Message History**. Open the URL it makes and invite the bot
   to your server.
4. In Discord, **User Settings → Advanced → Developer Mode** on; then right-click the
   gather channel, **Copy Channel ID**: that is `GATHER_CHANNEL_ID`.
5. The bot DMs players. A player whose DMs are closed to server members is named in
   the channel and can open them and use `!beta_info`.

## Environment

| variable | what | default |
|---|---|---|
| `GATHER_DISCORD_TOKEN` | the bot's token | required |
| `GATHER_CHANNEL_ID` | the one channel the bot listens and talks in | required |
| `GATHER_SERVERS` | the game servers, `name=host:port`, separated by spaces or commas; the name is what each server's script says (`GATHER_SERVER_NAME`), the host:port what players are told to join | required (or `GATHER_SERVER_ADDR` for one server, named `main`) |
| `GATHER_SECRET` | shared with every server's script; sent as a bearer token | required |
| `GATHER_LISTEN` | where the HTTP API listens | `:8080` |
| `GATHER_PREFIX` | the command prefix | `!beta_` |
| `GATHER_MAPS` | the map pool `!map` takes and the tiebreaker is drawn from, space-separated | SoldatReloaded's CTF maps |
| `GATHER_TEAM_SIZE` | players a side | `3` |

A `.env` in the working directory is read first ([.env.example](.env.example)).

## Running it

Locally:

```bash
cp .env.example .env    # fill it in
go run ./cmd/gatherbot
```

In Docker:

```bash
docker build -t gatherbot .
docker run --env-file .env -p 8080:8080 gatherbot
```

### On fly.io

One app. [fly.toml](fly.toml) has the settings that aren't secret; edit the app name,
the region, `GATHER_CHANNEL_ID` and `GATHER_SERVERS` there, then, from this directory:

```bash
fly launch --no-deploy --copy-config --name gather-bot
```

```bash
fly secrets set GATHER_DISCORD_TOKEN=... GATHER_SECRET=...
```

```bash
fly deploy --ha=false
```

`--ha=false` keeps it to one machine; Fly otherwise adds a second "for high
availability", which here would be a second bot answering every command twice.

The app's URL (`https://gather-bot.fly.dev`) is what the servers' `GATHER_BOT_URL`
points at. Servers in the same Fly organisation can use the private network instead,
`http://gather-bot.internal:8080`, and then the HTTP service needs no public address at
all (`fly ips release` the public ones). Either way every request carries the secret.

Useful afterwards:

```bash
fly logs
```

```bash
fly status
```

```bash
fly secrets set GATHER_SECRET=...   # rotating it: set the same on every server
```

Changing `GATHER_SERVERS` (adding a server) is an edit to fly.toml and a `fly deploy`;
the queue is lost on the restart.

## The API

The servers' scripts talk to the bot over HTTP, every request with
`Authorization: Bearer <secret>` and `X-Gather-Server: <name>` (the name may be left
out when the bot has one server):

| call | what |
|---|---|
| `GET /api/state` | `{server, version, gather_id, phase, password, tiebreaker, pool, teams}`; `phase` is `idle` or `live`, `password` what the server must ask, `tiebreaker` the third map, `pool` what `!map` may load. With `?since=<version>` the answer is held until the state changes (or 25 seconds pass): the script's long poll, so a change reaches the server at once and nothing is sent meanwhile |
| `POST /api/round` | a counted map's end: `{gather_id, map_index, map, why, scores, winner, players, done}` |
| `POST /api/event` | `{type: "join" or "leave", slot, name}`, for `!beta_status` to say who is on the server |
| `GET /healthz` | `ok`, no secret |

## Tests

```bash
go test ./...
```

The end-to-end test under `internal/e2e` runs the script on a real server against a
fake bot, typing `nextmap` and the chat commands at its console to run a three-map
series through. It needs the server's binary built from SoldatReloaded and runs only when
told where it is, with absolute paths:

```bash
GATHER_E2E_SERVER=/path/to/soldatreloaded/build/linux/x86_64/release/soldatreloaded-server GATHER_E2E_DIR=/path/to/soldatreloaded go test ./internal/e2e -v
```

## Layout

- `cmd/gatherbot` — main: config, the API server, the Discord session.
- `internal/gather` — one gather's state: teams, passwords, the tiebreaker, the series.
- `internal/service` — the queue, the servers, the commands and the reports, and what each says.
- `internal/api` — the HTTP side the scripts call.
- `internal/discord` — the commands read from the channel, announcements and DMs.
- `internal/config` — the environment and `.env`.
