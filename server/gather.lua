-- The gather script for a bettersoldat server (docs/scripting.md), the other half of
-- gatherbot. Copy it to the server's scripts/server.lua, or start the server with
-- +sv_script path/to/gather.lua, and set the values below or their environment
-- variables. One bot serves several servers: each says its name with every request.
--
-- It polls the bot for the gather's state every `poll_every` seconds and keeps the
-- server's password (sv_password) what the bot says it is: the bot makes a new one as a
-- gather starts, DMs it to the players, and makes another as it ends, so the server is
-- locked in between. It blocks map votes while a gather is on, and posts each counted
-- map's end back to the bot, which shows it in Discord.
--
-- The maps are picked in the game. While a gather is on, !map <name> starts a map
-- (a CTF map of the bot's pool); it counts once it runs to its end. !r replays the
-- map from the start, so the round it cuts short counts for nothing; !map another
-- map does the same. After two maps split 1-1, !tb plays the tiebreaker the bot
-- drew. The series is over after two maps with a team ahead, or after the third.
-- With no gather on, !map changes the map freely. Anyone but a spectator may say
-- those, and !p to pause and !up to count 3, 2, 1 and go on; anyone may say !status.

-- The settings, from the environment when it has them (the Docker image sets them),
-- else the values here.
local bot_url = os.getenv("GATHER_BOT_URL") or "http://127.0.0.1:8080" -- where gatherbot listens
local secret = os.getenv("GATHER_SECRET") or "change-me"               -- the bot's GATHER_SECRET
local server_name = os.getenv("GATHER_SERVER_NAME") or ""              -- this server's name in the bot's GATHER_SERVERS; "" with one server
local poll_every = tonumber(os.getenv("GATHER_POLL")) or 3             -- seconds between polls of the bot

local color = "7FD6FF"

-- --- the state ----------------------------------------------------------------------

local state = nil   -- the bot's last word: gather_id, phase, password, tiebreaker, pool, teams
local bot_down = false
local seconds = 0
local password_set = nil -- what sv_password was last set to

-- The series being played: how many maps have counted, the maps won, the map asked
-- for with !map or !tb and not yet started, and whether the round on now counts.
local series = {id = nil, tiebreaker = nil, played = 0, wins = {alpha = 0, bravo = 0},
                wanted = nil, counting = false, cut = false, done = true}

local countdown = nil -- ticks left until the game goes on, while !up counts

-- --- talking to the bot --------------------------------------------------------------

local function headers()
    return {Authorization = "Bearer " .. secret, ["Content-Type"] = "application/json",
            ["X-Gather-Server"] = server_name}
end

local function post(path, body)
    http.request({url = bot_url .. path, method = "POST", body = json.encode(body),
                  headers = headers(), timeout = 10},
        function(r)
            if r.error then server.print("gather: POST " .. path .. " failed: " .. r.error)
            elseif r.status >= 300 then server.print(("gather: POST %s answered %d: %s"):format(path, r.status, r.body or "")) end
        end)
end

local function event(kind, slot, name)
    post("/api/event", {type = kind, slot = slot, name = name})
end

-- --- the series ---------------------------------------------------------------------------

local function in_series() return series.id and not series.done end

local function tied() return series.wins.alpha == series.wins.bravo end

local function begin_series(st)
    series = {id = st.gather_id, tiebreaker = st.tiebreaker, played = 0, wins = {alpha = 0, bravo = 0},
              wanted = nil, counting = false, cut = false, done = false}
    server.print(("gather: #%d is on; the tiebreaker is %s"):format(st.gather_id, tostring(st.tiebreaker)))
    server.say(("Gather #%d is on! Say !map <map> to start map 1; a map counts once it runs to its end. The tiebreaker, at 1-1, is %s (!tb)."):format(
        st.gather_id, tostring(st.tiebreaker)), color)
end

local function end_series(why)
    if in_series() then
        server.print("gather: #" .. series.id .. " " .. why)
        server.say("Gather #" .. series.id .. " " .. why, color)
    end
    series.done = true
    series.counting = false
    series.wanted = nil
end

-- The server's password, as the bot says: set through the console, which reads it live.
local function set_password(pw)
    pw = pw or ""
    if pw == password_set then return end
    if pw:find("[%s\"]") then
        server.print("gather: the bot's password has a space or a quote in it; not set")
        return
    end
    server.command("sv_password " .. pw)
    password_set = pw
    server.print(pw == "" and "gather: the password is off" or "gather: the password is set")
end

local function apply(st)
    state = st
    set_password(st.password)
    if st.phase == "live" then
        if series.id ~= st.gather_id then begin_series(st) end
    elseif series.id then
        end_series("was ended in Discord")
    end
end

local function poll()
    http.request({url = bot_url .. "/api/state", headers = headers(), timeout = 5}, function(r)
        if r.error or r.status ~= 200 then
            if not bot_down then
                server.print("gather: the bot is unreachable: " .. (r.error or ("status " .. r.status)))
                bot_down = true
            end
            return
        end
        local ok, st = pcall(json.decode, r.body)
        if not ok or type(st) ~= "table" then
            server.print("gather: the bot's state did not parse")
            return
        end
        if bot_down then server.print("gather: the bot is back"); bot_down = false end
        apply(st)
    end)
end

-- --- the chat commands: !map, !tb, !r, !p, !up, !status --------------------------------

local function who(slot)
    local p = server.player(slot)
    return p and p.name or "someone"
end

local function spectating(slot)
    local p = server.player(slot)
    return p and p.spectator
end

-- The map `name` names in the bot's pool: as given, in any case, with or without the
-- ctf_ prefix, or as the one map it is a prefix of. The pool is the bot's, so only a
-- map that is there is ever loaded: the server stops on a map it can't load.
local function resolve_map(name)
    local pool = state and state.pool or {}
    local want = name:lower():gsub("^%s+", ""):gsub("%s+$", "")
    if want == "" then return nil end
    local bare = want:gsub("^ctf_", "")
    local prefixed = {}
    for _, m in ipairs(pool) do
        local low = m:lower()
        local lowbare = low:gsub("^ctf_", "")
        if low == want or lowbare == bare then return m end
        if lowbare:sub(1, #bare) == bare then prefixed[#prefixed + 1] = m end
    end
    if #prefixed == 1 then return prefixed[1] end
    return nil
end

-- Loads `map` as the series' next counted map (or freely, with no series on).
local function play(slot, map, what)
    if series.counting then
        series.cut = true
        server.say(("The round on %s is cut short and counts for nothing."):format(server.map()), color)
    end
    if in_series() then
        series.wanted = map
        server.say(("%s starts map %d of 3: %s%s"):format(who(slot), series.played + 1, map, what or ""), color)
    else
        server.say(("%s changes the map to %s"):format(who(slot), map), color)
    end
    server.next_map(map)
end

local function cmd_map(slot, name)
    if not state then
        server.say_to(slot, "The gather bot is not reachable, so no map list to check against.", color)
        return
    end
    if in_series() and series.played >= 2 then
        if tied() then server.say_to(slot, "It is 1-1: say !tb for the tiebreaker, " .. tostring(series.tiebreaker) .. ".", color)
        else server.say_to(slot, "The series is decided; wait for the bot to close it.", color) end
        return
    end
    local map = resolve_map(name or "")
    if not map then
        server.say_to(slot, "No CTF map called '" .. (name or "") .. "' in the pool.", color)
        return
    end
    play(slot, map)
end

local function cmd_tiebreaker(slot)
    if not in_series() then
        server.say_to(slot, "No gather is on.", color)
        return
    end
    if series.played ~= 2 or not tied() then
        server.say_to(slot, ("No tiebreaker yet: %d of 3 maps played, alpha %d - %d bravo."):format(
            series.played, series.wins.alpha, series.wins.bravo), color)
        return
    end
    if not series.tiebreaker or series.tiebreaker == "" then
        server.say_to(slot, "The bot drew no tiebreaker; !map one.", color)
        return
    end
    play(slot, series.tiebreaker, " (the tiebreaker)")
end

local function cmd_restart(slot)
    if in_series() and not series.counting then
        server.say_to(slot, "No map of the gather is on; !map <map> starts one.", color)
        return
    end
    local map = server.map()
    if series.counting then
        series.cut = true
        series.wanted = map -- the replay counts
    end
    server.say(("%s restarts the round on %s"):format(who(slot), map), color)
    server.next_map(map)
end

local function cmd_pause(slot)
    countdown = nil
    if server.pause() then server.say(("Game paused by %s. !up to go on."):format(who(slot)), color)
    else server.say_to(slot, "The game is already paused.", color) end
end

local function cmd_unpause(slot)
    if not server.paused() then
        server.say_to(slot, "The game is not paused.", color)
        return
    end
    if countdown then return end
    countdown = 3 * 60
    server.say("3", color)
end

local function cmd_status(slot)
    local lines = {}
    if not state then
        lines[#lines + 1] = "The gather bot is unreachable; no gather can start."
    elseif state.phase == "idle" then
        lines[#lines + 1] = ("Gather #%d: nobody playing yet; !beta_add in Discord to queue."):format(state.gather_id)
    else
        lines[#lines + 1] = ("Gather #%d: live"):format(state.gather_id)
        if state.teams then
            lines[#lines + 1] = ("Alpha: %s | Bravo: %s"):format(
                table.concat(state.teams.alpha or {}, ", "), table.concat(state.teams.bravo or {}, ", "))
        end
    end
    if in_series() then
        local now
        if series.counting then now = "this map counts"
        elseif series.wanted then now = series.wanted .. " is loading"
        elseif series.played >= 2 and tied() then now = "say !tb for the tiebreaker"
        else now = "say !map <map> for map " .. (series.played + 1) end
        lines[#lines + 1] = ("Series: alpha %d - %d bravo, %d of 3 maps played; %s. Tiebreaker: %s."):format(
            series.wins.alpha, series.wins.bravo, series.played, now, tostring(series.tiebreaker))
    end
    local s, left = server.scores(), math.floor(server.time_left())
    lines[#lines + 1] = ("Now on %s: alpha %d - %d bravo, %d:%02d left%s"):format(
        server.map(), s.alpha, s.bravo, left // 60, left % 60, server.paused() and ", paused" or "")
    for _, line in ipairs(lines) do server.say_to(slot, line, color) end
end

-- --- the hooks ---------------------------------------------------------------------------

function on_join(slot, name)
    local p = server.player(slot)
    if p and p.bot then return end
    if in_series() then
        server.say_to(slot, ("Gather #%d is on: join your team from the team menu (M), or watch from the spectators. !status for the score."):format(series.id), color)
    else
        server.say_to(slot, "Welcome. This server runs gathers from Discord; !map <map> to warm up meanwhile.", color)
    end
    event("join", slot, name)
end

function on_leave(slot, name)
    event("leave", slot, name)
end

function on_chat(slot, text, team)
    local lower = text:lower()
    if in_series() and lower:match("^/votemap") then
        server.say_to(slot, "No map votes during a gather.", color)
        return true
    end
    local cmd, arg = lower:match("^!(%a+)%s*(.*)$")
    if not cmd then return end
    if cmd == "status" then
        cmd_status(slot)
        return true
    end
    if spectating(slot) then
        server.say_to(slot, "Spectators don't run the game.", color)
        return true
    end
    if cmd == "map" then cmd_map(slot, arg)
    elseif cmd == "tb" or cmd == "tiebreaker" then cmd_tiebreaker(slot)
    elseif cmd == "r" or cmd == "restart" then cmd_restart(slot)
    elseif cmd == "p" or cmd == "pause" then cmd_pause(slot)
    elseif cmd == "up" or cmd == "unpause" then cmd_unpause(slot)
    else return end
    return true
end

-- Every second, by the server's own ticks (on_second stands still with the world).
local function every_second()
    seconds = seconds + 1
    if seconds % poll_every == 0 then poll() end
end

local ticks = 0

-- Every tick: the second's work by the server's own count, and the !up countdown,
-- as the world's clock stands while paused.
function on_tick(tick)
    ticks = ticks + 1
    if ticks % 60 == 0 then every_second() end
    if not countdown then return end
    countdown = countdown - 1
    if countdown == 2 * 60 then server.say("2", color)
    elseif countdown == 60 then server.say("1", color)
    elseif countdown <= 0 then
        countdown = nil
        server.unpause()
        server.say("Go!", color)
    end
end

-- A counted round's end: it counts unless !r or !map cut it short.
function on_round_end(stats)
    if not series.counting then return end
    series.counting = false
    if series.cut then
        series.cut = false
        return
    end
    series.played = series.played + 1
    if stats.winner == "alpha" or stats.winner == "bravo" then
        series.wins[stats.winner] = series.wins[stats.winner] + 1
    end
    series.done = series.played >= 3 or (series.played == 2 and not tied())
    local players = {}
    for _, p in ipairs(stats.players) do
        if not p.bot and not p.spectator then
            players[#players + 1] = {name = p.name, team = p.team, kills = p.kills, deaths = p.deaths,
                                     flags = p.flags, ping = p.ping}
        end
    end
    post("/api/round", {
        gather_id = series.id, map_index = series.played, map = stats.map, why = stats.why,
        scores = stats.scores, winner = stats.winner, players = json.array(players), done = series.done,
    })
    local next_step
    if series.done then
        next_step = "The series is over; the password changes, see Discord for the next one."
    elseif series.played == 2 then
        next_step = ("1-1: say !tb for the tiebreaker, %s."):format(tostring(series.tiebreaker))
    else
        next_step = "Say !map <map> for map 2."
    end
    server.say(("Map %d of 3 over on %s: alpha %d - %d bravo. Series alpha %d - %d bravo. %s"):format(
        series.played, stats.map, stats.scores.alpha, stats.scores.bravo, series.wins.alpha, series.wins.bravo, next_step), color)
end

-- The next round: the one asked for counts; any other (the rotation's) is warm-up.
function on_round_start(map)
    countdown = nil
    if in_series() and series.wanted and map == series.wanted then
        series.wanted = nil
        series.counting = true
        server.say(("Gather #%d, map %d of 3: %s. This one counts."):format(series.id, series.played + 1, map), color)
    else
        series.counting = false
    end
end

server.print(("gather: script loaded, polling %s as %s"):format(bot_url, server_name ~= "" and server_name or "the only server"))
poll()
