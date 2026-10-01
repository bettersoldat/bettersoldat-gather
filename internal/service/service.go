// Package service runs the gather: the commands from Discord on one side, the game
// server's reports on the other, with one lock around the state and the messages that
// each change calls for. It talks to Discord only through the Notifier.
package service

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"gatherbot/internal/api"
	"gatherbot/internal/gather"
)

// Notifier is Discord, as far as the service cares: a line in the gather channel, or
// a direct message to one user.
type Notifier interface {
	Announce(text string)
	DM(userID, text string) error
}

// Config is what the service needs to know.
type Config struct {
	ServerAddr  string        // host:port the players are told to join
	TeamSize    int           // 3 for 3v3
	Pool        []string      // the maps a team may pick
	PickTimeout time.Duration // how long the teams have to pick before the bot picks for them
	Prefix      string        // the command prefix, for the help and the hints
	Grace       int           // seconds a player has to say /pw before the server kicks; the script's own setting, told to players
}

// Service is the gather and everything around it.
type Service struct {
	mu        sync.Mutex
	cfg       Config
	g         *gather.Gather
	n         Notifier
	onServer  map[int]string // slot -> name, from the script's join and leave events
	pickTimer *time.Timer
}

// New makes an idle service. The notifier may be set later with SetNotifier, before
// anything happens.
func New(cfg Config, n Notifier) *Service {
	if cfg.TeamSize <= 0 {
		cfg.TeamSize = 3
	}
	if cfg.PickTimeout <= 0 {
		cfg.PickTimeout = 90 * time.Second
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "!beta_"
	}
	if cfg.Grace <= 0 {
		cfg.Grace = 30
	}
	return &Service{cfg: cfg, g: gather.New(cfg.TeamSize, cfg.Pool, nil), n: n, onServer: map[int]string{}}
}

// SetNotifier plugs Discord in.
func (s *Service) SetNotifier(n Notifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n = n
}

func (s *Service) announce(text string) {
	if s.n != nil {
		s.n.Announce(text)
	}
}

func (s *Service) cmd(name string) string { return s.cfg.Prefix + name }

// --- the commands: each answers with the reply to the one who asked ----------------

// Add puts p in the queue.
func (s *Service) Add(p gather.Player) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.g.Add(p, s.cfg.PickTimeout)
	if err != nil {
		return err.Error()
	}
	if !full {
		return fmt.Sprintf("added to gather #%d (%d/%d).", s.g.ID, len(s.g.Queue), s.g.Size())
	}
	s.started()
	return fmt.Sprintf("added: gather #%d is full!", s.g.ID)
}

// Del takes p out of the queue.
func (s *Service) Del(p gather.Player) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.g.Del(p); err != nil {
		return err.Error()
	}
	return fmt.Sprintf("removed from gather #%d (%d/%d).", s.g.ID, len(s.g.Queue), s.g.Size())
}

// Status tells where the gather stands.
func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.g
	var b strings.Builder
	switch g.Phase {
	case gather.Idle:
		fmt.Fprintf(&b, "**Gather #%d** — waiting for players (%d/%d)", g.ID, len(g.Queue), g.Size())
		if len(g.Queue) > 0 {
			fmt.Fprintf(&b, ": %s", gather.Names(g.Queue))
		}
		fmt.Fprintf(&b, "\nJoin with `%s`.", s.cmd("add"))
	case gather.Picking:
		fmt.Fprintf(&b, "**Gather #%d** — picking maps", g.ID)
		if left := time.Until(g.PickDeadline).Round(time.Second); left > 0 {
			fmt.Fprintf(&b, " (%s left)", left)
		}
		b.WriteString("\n")
		for t := range g.Teams {
			pick := "not picked yet"
			if g.Picks[t] != "" {
				pick = g.Picks[t]
			}
			fmt.Fprintf(&b, "%s: %s — %s\n", teamTitle(t), gather.Names(g.Teams[t]), pick)
		}
		fmt.Fprintf(&b, "Pick with `%s <map>`; `%s` lists them.", s.cmd("pick"), s.cmd("maps"))
	case gather.Live:
		fmt.Fprintf(&b, "**Gather #%d** — live on %s\n", g.ID, s.cfg.ServerAddr)
		for t := range g.Teams {
			fmt.Fprintf(&b, "%s: %s\n", teamTitle(t), gather.Names(g.Teams[t]))
		}
		b.WriteString(s.mapsLine())
		fmt.Fprintf(&b, "\nSeries: Alpha %d - %d Bravo (%d of %d maps played)", g.Wins[gather.Alpha], g.Wins[gather.Bravo], len(g.Results), len(g.Maps()))
	}
	if names := s.onServerNames(); len(names) > 0 {
		fmt.Fprintf(&b, "\nOn the server now: %s", strings.Join(names, ", "))
	}
	return b.String()
}

// Spec sends p what it takes to watch.
func (s *Service) Spec(p gather.Player) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.g.Phase == gather.Idle {
		return "there is no gather to watch right now."
	}
	msg := fmt.Sprintf("Spectating gather #%d\nServer: `%s`\nSpectator password: `%s`\n"+
		"Join, say `/pw %s` in the chat within %d seconds, and stay in the spectators (team menu, M). Taking a team gets you kicked.",
		s.g.ID, s.cfg.ServerAddr, s.g.SpecPassword, s.g.SpecPassword, s.cfg.Grace)
	if s.n == nil {
		return "no way to DM you."
	}
	if err := s.n.DM(p.ID, msg); err != nil {
		return "I couldn't DM you (are your DMs closed?)."
	}
	return "check your DMs."
}

// Info sends p its server info again, if p is playing.
func (s *Service) Info(p gather.Player) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.g.TeamOf(p)
	if t < 0 || s.g.Phase == gather.Idle {
		return "you are not in a running gather."
	}
	if s.n == nil {
		return "no way to DM you."
	}
	if err := s.n.DM(p.ID, s.playerDM(t)); err != nil {
		return "I couldn't DM you (are your DMs closed?)."
	}
	return "check your DMs."
}

// Pick records p's team's map.
func (s *Service) Pick(p gather.Player, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(name) == "" {
		return fmt.Sprintf("which map? `%s <map>`; `%s` lists them.", s.cmd("pick"), s.cmd("maps"))
	}
	team, picked, ready, err := s.g.Pick(p, name)
	if err != nil {
		if errors.Is(err, gather.ErrNoSuchMap) {
			return fmt.Sprintf("no map called %q in the pool; `%s` lists them.", name, s.cmd("maps"))
		}
		return err.Error()
	}
	if ready {
		s.stopPickTimer()
		s.announce(fmt.Sprintf("%s picks **%s**.", teamTitle(team), picked))
		s.live()
		return "picked."
	}
	return fmt.Sprintf("%s picks **%s**. Waiting for %s.", teamTitle(team), picked, teamTitle(1-team))
}

// Maps lists the pool.
func (s *Service) Maps() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "Map pool: " + strings.Join(s.g.SortedPool(), ", ")
}

// Abort ends the gather, whatever its state. admin says whether p may do so without
// being in it.
func (s *Service) Abort(p gather.Player, admin bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !admin && s.g.TeamOf(p) < 0 {
		return "only a player of this gather, or someone with Manage Server, can abort it."
	}
	if s.g.Phase == gather.Idle && len(s.g.Queue) == 0 {
		return "nothing to abort."
	}
	id := s.g.ID
	s.stopPickTimer()
	s.g.Reset()
	s.announce(fmt.Sprintf("Gather #%d aborted by %s. The server's password has been changed; `%s` to queue for #%d.", id, p.Name, s.cmd("add"), s.g.ID))
	return "aborted."
}

// Help lists the commands.
func (s *Service) Help() string {
	c := s.cmd
	return strings.Join([]string{
		fmt.Sprintf("`%s` — join the queue (%dv%d CTF)", c("add"), s.cfg.TeamSize, s.cfg.TeamSize),
		fmt.Sprintf("`%s` — leave the queue", c("del")),
		fmt.Sprintf("`%s` — where the gather stands", c("status")),
		fmt.Sprintf("`%s <map>` — your team's map, once the gather is full", c("pick")),
		fmt.Sprintf("`%s` — the map pool", c("maps")),
		fmt.Sprintf("`%s` — get the server info to spectate", c("spec")),
		fmt.Sprintf("`%s` — get your server info again", c("info")),
		fmt.Sprintf("`%s` — abort the running gather", c("abort")),
	}, "\n")
}

// --- what the server's script calls ----------------------------------------------

// State is the API's view.
func (s *Service) State() api.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := api.State{
		GatherID:     s.g.ID,
		Phase:        s.g.Phase.String(),
		Password:     s.g.Password,
		SpecPassword: s.g.SpecPassword,
		Maps:         s.g.Maps(),
		Pool:         s.g.SortedPool(),
		Teams:        map[string][]string{},
	}
	if st.Maps == nil {
		st.Maps = []string{}
	}
	for t := range s.g.Teams {
		names := []string{}
		for _, p := range s.g.Teams[t] {
			names = append(names, p.Name)
		}
		st.Teams[gather.TeamName(t)] = names
	}
	return st
}

// RoundEnd takes a round's report: it is told in the channel, and the series ended
// when it is over.
func (s *Service) RoundEnd(r gather.RoundReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	done, err := s.g.RoundEnd(r)
	if err != nil {
		return err
	}
	s.announce(s.roundText(r))
	if done {
		s.finish()
	}
	return nil
}

// ServerEvent notes who is on the server.
func (s *Service) ServerEvent(ev api.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Type {
	case "join":
		s.onServer[ev.Slot] = ev.Name
	case "leave":
		delete(s.onServer, ev.Slot)
	}
}

// --- what the changes call for ----------------------------------------------------

// started: the gather just filled. Everyone is DMed the server, the teams are told,
// and the picking clock runs.
func (s *Service) started() {
	g := s.g
	var failed []string
	for t := range g.Teams {
		for _, p := range g.Teams[t] {
			if s.n == nil {
				continue
			}
			if err := s.n.DM(p.ID, s.playerDM(t)); err != nil {
				log.Printf("service: DM to %s (%s): %v", p.Name, p.ID, err)
				failed = append(failed, p.Name)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Gather #%d is full!**\n", g.ID)
	for t := range g.Teams {
		fmt.Fprintf(&b, "%s: %s\n", teamTitle(t), gather.Names(g.Teams[t]))
	}
	fmt.Fprintf(&b, "The server info has been DMed to everyone. Each team picks a map with `%s <map>` within %s (`%s` lists them); the bot picks the tiebreaker.",
		s.cmd("pick"), s.cfg.PickTimeout, s.cmd("maps"))
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\nI couldn't DM %s: open your DMs and use `%s`.", strings.Join(failed, ", "), s.cmd("info"))
	}
	s.announce(b.String())

	id := g.ID
	s.pickTimer = time.AfterFunc(s.cfg.PickTimeout, func() { s.pickTimedOut(id) })
}

func (s *Service) stopPickTimer() {
	if s.pickTimer != nil {
		s.pickTimer.Stop()
		s.pickTimer = nil
	}
}

// pickTimedOut: the clock ran out on gather id's picking.
func (s *Service) pickTimedOut(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.g.ID != id || s.g.Phase != gather.Picking {
		return
	}
	forced, ready := s.g.PickTimeout()
	if !ready {
		return
	}
	teams := make([]int, 0, len(forced))
	for t := range forced {
		teams = append(teams, t)
	}
	sort.Ints(teams)
	for _, t := range teams {
		s.announce(fmt.Sprintf("Time's up: %s gets **%s**.", teamTitle(t), forced[t]))
	}
	s.live()
}

// live: the maps are settled; the script picks them up on its next poll.
func (s *Service) live() {
	var b strings.Builder
	fmt.Fprintf(&b, "**Gather #%d is live!**\n%s\nServer: `%s` — say `/pw <your password>` in the chat once you join (it is in your DMs; `%s` sends it again). Watch with `%s`.",
		s.g.ID, s.mapsLine(), s.cfg.ServerAddr, s.cmd("info"), s.cmd("spec"))
	s.announce(b.String())
}

// finish: the series is over. The passwords change, so the server is locked, and the
// queue opens for the next gather.
func (s *Service) finish() {
	g := s.g
	id := g.ID
	var result string
	switch w := g.SeriesWinner(); w {
	case -1:
		result = fmt.Sprintf("a tie, %d - %d", g.Wins[gather.Alpha], g.Wins[gather.Bravo])
	default:
		result = fmt.Sprintf("**%s wins** %d - %d", teamTitle(w), g.Wins[w], g.Wins[1-w])
	}
	s.stopPickTimer()
	g.Reset()
	s.announce(fmt.Sprintf("**Gather #%d is over:** %s. The server's password has been changed. `%s` to queue for gather #%d.", id, result, s.cmd("add"), g.ID))
}

// --- the texts -------------------------------------------------------------------------

func teamTitle(t int) string {
	if t == gather.Alpha {
		return "Alpha"
	}
	return "Bravo"
}

// playerDM is what a player of team t is sent as the gather fills.
func (s *Service) playerDM(t int) string {
	g := s.g
	var mates []string
	for _, q := range g.Teams[t] {
		mates = append(mates, q.Name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Gather #%d is on!** You are on team **%s** with %s.\n", g.ID, teamTitle(t), strings.Join(mates, ", "))
	fmt.Fprintf(&b, "Server: `%s`\nPassword: `%s`\n", s.cfg.ServerAddr, g.Password)
	fmt.Fprintf(&b, "Join the server, then say `/pw %s` in the chat (T) within %d seconds, or it kicks you. Then join team %s from the team menu (M).\n",
		g.Password, s.cfg.Grace, teamTitle(t))
	if g.Phase == gather.Picking {
		fmt.Fprintf(&b, "Your team picks its map in the channel: `%s <map>`.", s.cmd("pick"))
	} else {
		b.WriteString(s.mapsLine())
	}
	return b.String()
}

// mapsLine lists the series' maps, once live.
func (s *Service) mapsLine() string {
	maps := s.g.Maps()
	if maps == nil {
		return "Maps: not picked yet."
	}
	return fmt.Sprintf("Maps: 1. **%s** (Alpha's pick) 2. **%s** (Bravo's pick) 3. **%s** (tiebreaker, if needed)", maps[0], maps[1], maps[2])
}

// roundText tells a round's end: the score, the series, and everyone's line.
func (s *Service) roundText(r gather.RoundReport) string {
	g := s.g
	var b strings.Builder
	fmt.Fprintf(&b, "**Gather #%d, map %d/%d — %s:** Alpha %d - %d Bravo", r.GatherID, r.MapIndex, len(g.Maps()), r.Map, r.Scores.Alpha, r.Scores.Bravo)
	switch r.Winner {
	case "alpha", "bravo":
		fmt.Fprintf(&b, " — %s wins the map", teamTitle(teamIndex(r.Winner)))
	default:
		b.WriteString(" — a draw")
	}
	if r.Why != "" && r.Why != "limit" {
		fmt.Fprintf(&b, " (ended by %s)", r.Why)
	}
	fmt.Fprintf(&b, ". Series: Alpha %d - %d Bravo.", g.Wins[gather.Alpha], g.Wins[gather.Bravo])
	if len(r.Players) > 0 {
		players := append([]gather.PlayerStats(nil), r.Players...)
		sort.SliceStable(players, func(i, j int) bool {
			if players[i].Team != players[j].Team {
				return players[i].Team < players[j].Team
			}
			return players[i].Kills > players[j].Kills
		})
		b.WriteString("\n```\n")
		fmt.Fprintf(&b, "%-20s %-6s %4s %4s %4s %5s\n", "player", "team", "K", "D", "caps", "ping")
		for _, p := range players {
			fmt.Fprintf(&b, "%-20.20s %-6s %4d %4d %4d %5d\n", p.Name, p.Team, p.Kills, p.Deaths, p.Flags, p.Ping)
		}
		b.WriteString("```")
	}
	return b.String()
}

func teamIndex(name string) int {
	if name == "alpha" {
		return gather.Alpha
	}
	return gather.Bravo
}

func (s *Service) onServerNames() []string {
	names := make([]string, 0, len(s.onServer))
	for _, n := range s.onServer {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
