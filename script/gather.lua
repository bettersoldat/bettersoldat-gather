-- The gather script for a bettersoldat server (docs/scripting.md), the other half of
-- gatherbot. Copy it to the server's scripts/server.lua, or start the server with
-- +sv_script path/to/gather.lua, and set the three values below.
--
-- It keeps the server to the gather: whoever joins must say /pw <password> in the chat
-- within `grace` seconds, with the password the bot DMed them (or the spectators'
-- one, which keeps them out of the teams), or is kicked. It polls the bot for the
-- gather's state every `poll_every` seconds, plays the gather's maps in order when one
-- goes live (the tiebreaker only on 1-1), blocks map votes meanwhile, and posts each
-- round's end back to the bot, which shows it in Discord.
--
-- In the chat, anyone but a spectator may say !map <name> (a CTF map of the bot's
-- pool, between gathers), !p to pause, !up to count 3, 2, 1 and go on, and !r to
-- replay the current map from the start, which in a gather counts for nothing.

local bot_url = "http://127.0.0.1:8080" -- where gatherbot listens (GATHER_LISTEN)
local secret = "change-me"              -- the same as the bot's GATHER_SECRET
local grace = 30                        -- seconds to say /pw; the bot's GATHER_GRACE
local poll_every = 3                    -- seconds between polls of the bot

local color = "7FD6FF"

-- --- the state ----------------------------------------------------------------------

local state = nil   -- the bot's last word: gather_id, phase, password, spec_password, maps, teams
local bot_down = false
local seconds = 0

-- the series being played: the maps, which is on, and the maps won
local series = {id = nil, maps = {}, index = 0, ended = 0, playing = false, done = true,
                wins = {alpha = 0, bravo = 0}}

local auth = {}      -- slot -> "play" or "spec", once /pw was said right
local joined_at = {} -- slot -> os.time() of the join

-- --- talking to the bot --------------------------------------------------------------

local function headers()
    return {Authorization = "Bearer " .. secret, ["Content-Type"] = "application/json"}
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

local function begin_series(st)
    series = {id = st.gather_id, maps = st.maps, index = 1, ended = 0, playing = false, done = false,
              wins = {alpha = 0, bravo = 0}}
    server.print(("gather: #%d is live: %s"):format(st.gather_id, table.concat(st.maps, ", ")))
    server.say(("Gather #%d is live! %s, then %s, then %s if it is 1-1."):format(
        st.gather_id, st.maps[1], st.maps[2], st.maps[3] or "nothing"), color)
    server.next_map(st.maps[1])
end

local function abort_series(why)
    if series.id and not series.done then
        server.print("gather: #" .. series.id .. " " .. why)
        server.say("Gather #" .. series.id .. " " .. why, color)
    end
    series.done = true
    series.playing = false
end

local function apply(st)
    local was = state
    state = st
    if st.phase == "live" and type(st.maps) == "table" and #st.maps >= 2 then
        if series.id ~= st.gather_id then begin_series(st) end
    elseif series.id == st.gather_id or (series.id and st.gather_id ~= series.id) then
        -- the bot ended or aborted what we were playing
        abort_series("was ended in Discord")
    end
    if was and was.password ~= st.password then server.print("gather: the password has changed") end
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

-- --- the hooks ---------------------------------------------------------------------------

function on_join(slot, name)
    local p = server.player(slot)
    if p and p.bot then return end
    joined_at[slot] = os.time()
    auth[slot] = nil
    server.say_to(slot, ("This server is for gathers. Say /pw <password> in the chat within %d seconds (the password is in your Discord DMs)."):format(grace), color)
    event("join", slot, name)
end

function on_leave(slot, name)
    joined_at[slot] = nil
    auth[slot] = nil
    event("leave", slot, name)
end

function on_command(slot, text)
    local pw = text:match("^pw%s+(%S+)")
    if not pw then return false end
    if not state then
        server.say_to(slot, "The gather bot is not reachable right now; try again in a moment.", color)
    elseif pw == state.password then
        auth[slot] = "play"
        server.say_to(slot, "Welcome to the gather. Join your team from the team menu (M).", color)
    elseif pw == state.spec_password then
        auth[slot] = "spec"
        server.say_to(slot, "Welcome, spectator. Stay in the spectators: taking a team gets you kicked.", color)
    else
        server.say_to(slot, "Wrong password.", color)
    end
    return true
end

-- --- the chat commands: !map, !p, !up, !r ---------------------------------------------

local countdown = nil -- ticks left until the game goes on, while !up counts
local restarting = false -- !r: the round ending now is replayed, not counted

local function in_series() return series.id and not series.done end

local function who(slot)
    local p = server.player(slot)
    return p and p.name or "someone"
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

local function cmd_map(slot, name)
    if in_series() then
        server.say_to(slot, "A gather is on; its maps are set. !r replays the current one.", color)
        return
    end
    if not state then
        server.say_to(slot, "The gather bot is not reachable, so no map list to check against.", color)
        return
    end
    local map = resolve_map(name or "")
    if not map then
        server.say_to(slot, "No CTF map called '" .. (name or "") .. "' in the pool.", color)
        return
    end
    server.say(("%s changes the map to %s"):format(who(slot), map), color)
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

local function cmd_restart(slot)
    if restarting then return end
    if in_series() and not series.playing then
        server.say_to(slot, "Wait for the gather's map to load.", color)
        return
    end
    restarting = true
    server.say(("%s restarts the round on %s"):format(who(slot), server.map()), color)
    server.next_map(server.map())
end

function on_chat(slot, text, team)
    local lower = text:lower()
    if in_series() and lower:match("^/votemap") then
        server.say_to(slot, "No map votes during a gather.", color)
        return true
    end
    local cmd, arg = lower:match("^!(%a+)%s*(.*)$")
    if not cmd then return end
    if auth[slot] == "spec" then
        server.say_to(slot, "Spectators don't run the game.", color)
        return true
    end
    if cmd == "map" then cmd_map(slot, arg)
    elseif cmd == "p" or cmd == "pause" then cmd_pause(slot)
    elseif cmd == "up" or cmd == "unpause" then cmd_unpause(slot)
    elseif cmd == "r" or cmd == "restart" then cmd_restart(slot)
    else return end
    return true
end

-- Every second, by the server's own ticks (on_second stands still with the world).
local function every_second()
    seconds = seconds + 1
    if seconds % poll_every == 0 then poll() end
    local now = os.time()
    for _, p in ipairs(server.players()) do
        if not p.bot then
            if not auth[p.slot] then
                if not joined_at[p.slot] then joined_at[p.slot] = now end
                if now - joined_at[p.slot] >= grace then
                    server.kick(p.slot, "This server is for gathers: say /pw <password>")
                end
            elseif auth[p.slot] == "spec" and (p.team == "alpha" or p.team == "bravo") then
                server.kick(p.slot, "Spectators only")
            end
        end
    end
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

function on_match_end(winner)
    if not series.playing then return end
    if restarting then return end -- !r: the round is replayed, not counted
    series.ended = series.index
    if winner == "alpha" or winner == "bravo" then series.wins[winner] = series.wins[winner] + 1 end
    local nxt
    if series.index == 1 then nxt = 2
    elseif series.index == 2 and series.wins.alpha == series.wins.bravo then nxt = 3 end
    if nxt and series.maps[nxt] then
        series.index = nxt
        server.next_map(series.maps[nxt])
    else
        series.done = true
    end
end

function on_round_end(stats)
    if restarting then
        series.playing = false
        return
    end
    if not series.playing then return end
    series.playing = false
    local players = {}
    for _, p in ipairs(stats.players) do
        if not p.bot and not p.spectator then
            players[#players + 1] = {name = p.name, team = p.team, kills = p.kills, deaths = p.deaths,
                                     flags = p.flags, ping = p.ping}
        end
    end
    post("/api/round", {
        gather_id = series.id, map_index = series.ended, map = stats.map, why = stats.why,
        scores = stats.scores, winner = stats.winner, players = json.array(players), done = series.done,
    })
    if series.done then
        server.say(("Gather #%d is over: alpha %d - %d bravo. The password has changed; see Discord for the next one."):format(
            series.id, series.wins.alpha, series.wins.bravo), color)
    end
end

function on_round_start(map)
    countdown = nil
    local restarted = restarting
    restarting = false
    if not series.id or series.done then return end
    local wanted = series.maps[series.index]
    if map == wanted then
        series.playing = true
        series.retries = 0
        server.say(("Gather #%d, map %d of %d: %s%s"):format(series.id, series.index, #series.maps, map,
            restarted and " (restarted)" or ""), color)
        return
    end
    -- The rotation got there first (a round ended by `nextmap` or a vote is settled
    -- before the script hears of it): switch to the gather's map now.
    series.retries = (series.retries or 0) + 1
    if series.retries > 3 then
        abort_series("could not switch to " .. wanted .. "; is the map there?")
        return
    end
    server.print(("gather: wanted %s, got %s; switching"):format(wanted, map))
    server.next_map(wanted)
end

server.print("gather: script loaded, polling " .. bot_url)
poll()
