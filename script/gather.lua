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

function on_chat(slot, text, team)
    if series.id and not series.done and text:lower():match("^/votemap") then
        server.say_to(slot, "No map votes during a gather.", color)
        return true
    end
end

function on_second()
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

function on_match_end(winner)
    if not series.playing then return end
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
    if not series.id or series.done then return end
    local wanted = series.maps[series.index]
    if map == wanted then
        series.playing = true
        series.retries = 0
        server.say(("Gather #%d, map %d of %d: %s"):format(series.id, series.index, #series.maps, map), color)
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
