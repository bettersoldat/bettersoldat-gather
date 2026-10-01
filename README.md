# gather-bot

A Discord gather bot for [bettersoldat](https://github.com/bettersoldat/bettersoldat),
in two halves that deploy on their own:

- [bot/](bot/README.md) — the Discord bot, in Go: the queue, the teams, the map picks,
  the passwords, the round reports. One of these.
- [server/](server/README.md) — a bettersoldat dedicated server with the gather script
  on it, as a Docker image for fly.io. As many of these as you like; each tells the bot
  its name, and the bot runs one gather on each.

Each has a Dockerfile, a fly.toml and a README with its setup: the bot's covers the
Discord application and its environment, the server's the fly.io commands and what UDP
needs there.

3v3 CTF, best of three: `!beta_add` to queue; in the game `!map <map>` starts a map,
`!r` replays it, and at 1-1 `!tb` plays the tiebreaker the bot drew. No rankings,
nothing persisted.
