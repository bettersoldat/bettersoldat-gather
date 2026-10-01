# gatherbot

A Discord gather bot for [bettersoldat](../bettersoldat), in Go, with the server-side
half as a Lua script for the dedicated server. 3v3 CTF, best of three: each team picks a
map, the bot picks the tiebreaker, and the server plays it only when the first two are
split 1-1. No rankings, nothing persisted: restart the bot and the queue is empty.

## How a gather goes

1. Players `!beta_add` in the gather channel. `!beta_del` leaves, `!beta_status` shows
   the queue.
2. At six, the bot shuffles them into **Alpha** and **Bravo**, makes a new random
   password for the server and a second one for spectators, and DMs each player the
   server's address, their password and their team.
3. Each team has 90 seconds to `!beta_pick <map>` (`!beta_maps` lists the pool; a team
   may change its pick until time is up). The bot picks for a team that doesn't, and
   draws the tiebreaker from the rest of the pool.
4. The gather is live. The server's script sees that on its next poll, switches to
   Alpha's map, then Bravo's, then the tiebreaker if it is 1-1, and posts every round's
   end: the bot shows the score, the series and everyone's kills, deaths, caps and ping.
5. When the series is over the bot changes both passwords again, so the server is locked,
   and opens the queue for the next gather. `!beta_abort` does the same at any point
   (for a player of the gather, or anyone with Manage Server).

`!beta_spec` DMs the server and the spectators' password. `!beta_info` sends a player
their DM again. `!beta_help` lists the commands.

## In the game

Players run a few things from the chat, whatever the gather is doing (spectators
can't):

| said | what |
|---|---|
| `!map <name>` | loads a CTF map of the pool (`ash` finds ctf_Ash), between gathers; during one the maps are set |
| `!p` | pauses the game |
| `!up` | counts 3, 2, 1 and goes on |
| `!r` | replays the current map from the start; in a gather the round so far doesn't count |

## The password

bettersoldat's server has no password of its own, so the script keeps the door: anyone
who joins must say `/pw <password>` in the chat within 30 seconds or is kicked. The
players' password lets them into the teams; the spectators' keeps them in the
spectators (taking a team gets them kicked). Bots are left alone. While a gather is on,
`/votemap` is refused.

## Running it

The bot:

```bash
cp .env.example .env    # then fill in the token, the channel, the server's address and a secret
go run ./cmd/gatherbot
```

The Discord application needs the **Message Content** intent (Bot settings in the
developer portal), and in the server the bot needs to read and send messages in the
gather channel. Invite it with the `bot` scope.

The server, from bettersoldat's directory, with the script and a gather's limits:

```bash
./bettersoldat-server +sv_script ../gather-bot/script/gather.lua +sv_timelimit 10 +sv_killlimit 10
```

Set `bot_url`, `secret` and (if changed) `grace` at the top of
[script/gather.lua](script/gather.lua) first. The script polls the bot every 3 seconds,
so the bot must be reachable from the game server; put it on the same machine, or
behind a host the server can reach. The time and kill limits are the server's own and
read as it starts, so set them on the command line or in its `config.cfg`.

## The API

The script talks to the bot over HTTP, every request with `Authorization: Bearer
<secret>`:

| call | what |
|---|---|
| `GET /api/state` | `{gather_id, phase, password, spec_password, maps, teams}`; `phase` is `idle`, `picking` or `live`, `maps` is alpha's pick, bravo's and the tiebreaker once live |
| `POST /api/round` | a round's end: `{gather_id, map_index, map, why, scores, winner, players, done}` |
| `POST /api/event` | `{type: "join" or "leave", slot, name}`, for `!beta_status` to say who is on the server |
| `GET /healthz` | `ok`, no secret |

## Tests

```bash
go test ./...
```

The end-to-end test under `internal/e2e` runs the script on a real server against a
fake bot, typing `nextmap` at its console to run a three-map series through. It needs
the server's binary (built after bettersoldat's scripting commit) and runs only when
told where it is:

```bash
GATHER_E2E_SERVER=../bettersoldat/build/windows/x64/release/bettersoldat-server.exe GATHER_E2E_DIR=../bettersoldat go test ./internal/e2e -v
```

## Layout

- `cmd/gatherbot` — main: config, the API server, the Discord session.
- `internal/gather` — the gather's state: queue, teams, picks, passwords, the series.
- `internal/service` — the commands and the server's reports, and what each says.
- `internal/api` — the HTTP side the script calls.
- `internal/discord` — the commands read from the channel, announcements and DMs.
- `internal/config` — the environment and `.env`.
- `script/gather.lua` — the server's script.
