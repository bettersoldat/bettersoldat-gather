// Package service runs the gathers: one queue, and a gather on each game server. The
// commands from Discord come in on one side, the servers' scripts' reports on the
// other, with one lock around the state and the messages that each change calls for.
// It talks to Discord only through the Notifier.
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

// ServerConfig is one game server: its name, which its script says with every
// request, and the host:port the players are told to join.
type ServerConfig struct {
	Name string
	Addr string
}

// Config is what the service needs to know.
type Config struct {
	Servers     []ServerConfig
	TeamSize    int           // 3 for 3v3
	Pool        []string      // the maps a team may pick
	PickTimeout time.Duration // how long the teams have to pick before the bot picks for them
	Prefix      string        // the command prefix, for the help and the hints
	Grace       int           // seconds a player has to say /pw before the server kicks; the script's own setting, told to players
}

// server is one game server and the gather on it.
type server struct {
	name      string
	addr      string
	g         *gather.Gather
	onServer  map[int]string // slot -> name, from the script's join and leave events
	pickTimer *time.Timer
}

// Service is the queue, the servers and everything around them.
type Service struct {
	mu      sync.Mutex
	cfg     Config
	servers []*server // in the configured order; the first idle one takes the next gather
	byName  map[string]*server
	queue   []gather.Player
	nextID  int
	n       Notifier
}

// Errors the commands answer with.
var (
	ErrInQueue    = errors.New("you are already in the queue")
	ErrNotInQueue = errors.New("you are not in the queue")
	ErrNoServer   = errors.New("no such server")
)

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
	if len(cfg.Servers) == 0 {
		panic("service: at least one server is needed")
	}
	s := &Service{cfg: cfg, byName: map[string]*server{}, nextID: 1, n: n}
	for _, sc := range cfg.Servers {
		srv := &server{name: sc.Name, addr: sc.Addr, g: gather.New(cfg.TeamSize, cfg.Pool, nil), onServer: map[int]string{}}
		s.servers = append(s.servers, srv)
		s.byName[strings.ToLower(sc.Name)] = srv
	}
	return s
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

func (s *Service) size() int { return 2 * s.cfg.TeamSize }

// title is "Gather #3", or "Gather #3 on eu1" with more than one server.
func (s *Service) title(srv *server) string {
	if len(s.servers) == 1 {
		return fmt.Sprintf("Gather #%d", srv.g.ID)
	}
	return fmt.Sprintf("Gather #%d on %s", srv.g.ID, srv.name)
}

// serverOf is the server whose running gather p plays in, or nil.
func (s *Service) serverOf(p gather.Player) *server {
	for _, srv := range s.servers {
		if srv.g.Phase != gather.Idle && srv.g.TeamOf(p) >= 0 {
			return srv
		}
	}
	return nil
}

// idleServer is the first server with no gather on, or nil.
func (s *Service) idleServer() *server {
	for _, srv := range s.servers {
		if srv.g.Phase == gather.Idle {
			return srv
		}
	}
	return nil
}

// active is every server with a gather on.
func (s *Service) active() []*server {
	var out []*server
	for _, srv := range s.servers {
		if srv.g.Phase != gather.Idle {
			out = append(out, srv)
		}
	}
	return out
}

// lookup finds a server by name; "" means the only one, or the only active one.
func (s *Service) lookup(name string) (*server, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "" {
		if srv, ok := s.byName[name]; ok {
			return srv, nil
		}
		return nil, fmt.Errorf("%w %q; the servers are %s", ErrNoServer, name, s.serverNames())
	}
	if len(s.servers) == 1 {
		return s.servers[0], nil
	}
	if active := s.active(); len(active) == 1 {
		return active[0], nil
	}
	return nil, fmt.Errorf("which server? %s", s.serverNames())
}

func (s *Service) serverNames() string {
	names := make([]string, len(s.servers))
	for i, srv := range s.servers {
		names[i] = srv.name
	}
	return strings.Join(names, ", ")
}

// --- the commands: each answers with the reply to the one who asked ----------------

// Add puts p in the queue. When that fills a gather and a server is free, it starts.
func (s *Service) Add(p gather.Player) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv := s.serverOf(p); srv != nil {
		return fmt.Sprintf("you are already playing in %s.", strings.ToLower(s.title(srv)))
	}
	for _, q := range s.queue {
		if q.ID == p.ID {
			return ErrInQueue.Error()
		}
	}
	s.queue = append(s.queue, p)
	if len(s.queue) < s.size() {
		return fmt.Sprintf("added to the queue (%d/%d).", len(s.queue), s.size())
	}
	srv := s.idleServer()
	if srv == nil {
		s.announce(fmt.Sprintf("The queue is full, but every server is busy: the gather starts as soon as one frees up (`%s` shows them).", s.cmd("status")))
		return "added: the queue is full, waiting for a free server."
	}
	s.start(srv)
	return fmt.Sprintf("added: %s is full!", strings.ToLower(s.title(srv)))
}

// Del takes p out of the queue.
func (s *Service) Del(p gather.Player) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, q := range s.queue {
		if q.ID == p.ID {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			return fmt.Sprintf("removed from the queue (%d/%d).", len(s.queue), s.size())
		}
	}
	if srv := s.serverOf(p); srv != nil {
		return fmt.Sprintf("you are playing in %s; `%s` ends it.", strings.ToLower(s.title(srv)), s.cmd("abort"))
	}
	return ErrNotInQueue.Error()
}

// Status tells where the queue and every server stand.
func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "**Queue** %d/%d", len(s.queue), s.size())
	if len(s.queue) > 0 {
		fmt.Fprintf(&b, ": %s", gather.Names(s.queue))
	}
	fmt.Fprintf(&b, " — `%s` to join.", s.cmd("add"))
	for _, srv := range s.servers {
		b.WriteString("\n")
		s.serverStatus(&b, srv)
	}
	return b.String()
}

func (s *Service) serverStatus(b *strings.Builder, srv *server) {
	g := srv.g
	head := fmt.Sprintf("**%s** (`%s`)", srv.name, srv.addr)
	if len(s.servers) == 1 {
		head = fmt.Sprintf("**Server** `%s`", srv.addr)
	}
	switch g.Phase {
	case gather.Idle:
		fmt.Fprintf(b, "%s — free", head)
	case gather.Picking:
		fmt.Fprintf(b, "%s — gather #%d, picking maps", head, g.ID)
		if left := time.Until(g.PickDeadline).Round(time.Second); left > 0 {
			fmt.Fprintf(b, " (%s left)", left)
		}
		for t := range g.Teams {
			pick := "not picked yet"
			if g.Picks[t] != "" {
				pick = g.Picks[t]
			}
			fmt.Fprintf(b, "\n%s: %s — %s", teamTitle(t), gather.Names(g.Teams[t]), pick)
		}
		fmt.Fprintf(b, "\nPick with `%s <map>`; `%s` lists them.", s.cmd("pick"), s.cmd("maps"))
	case gather.Live:
		fmt.Fprintf(b, "%s — gather #%d, live", head, g.ID)
		for t := range g.Teams {
			fmt.Fprintf(b, "\n%s: %s", teamTitle(t), gather.Names(g.Teams[t]))
		}
		fmt.Fprintf(b, "\n%s\nSeries: Alpha %d - %d Bravo (%d of %d maps played)", mapsLine(g), g.Wins[gather.Alpha], g.Wins[gather.Bravo], len(g.Results), len(g.Maps()))
	}
	if names := onServerNames(srv); len(names) > 0 {
		fmt.Fprintf(b, "\nOn the server now: %s", strings.Join(names, ", "))
	}
}

// Spec sends p what it takes to watch the gather on server name ("" for the one
// there is).
func (s *Service) Spec(p gather.Player, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active()) == 0 {
		return "there is no gather to watch right now."
	}
	srv, err := s.lookup(name)
	if err != nil {
		return err.Error() + fmt.Sprintf(" (`%s <server>`).", s.cmd("spec"))
	}
	if srv.g.Phase == gather.Idle {
		return fmt.Sprintf("nothing is on at %s right now.", srv.name)
	}
	msg := fmt.Sprintf("Spectating %s\nServer: `%s`\nSpectator password: `%s`\n"+
		"Join, say `/pw %s` in the chat within %d seconds, and stay in the spectators (team menu, M). Taking a team gets you kicked.",
		s.title(srv), srv.addr, srv.g.SpecPassword, srv.g.SpecPassword, s.cfg.Grace)
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
	srv := s.serverOf(p)
	if srv == nil {
		return "you are not in a running gather."
	}
	if s.n == nil {
		return "no way to DM you."
	}
	if err := s.n.DM(p.ID, s.playerDM(srv, srv.g.TeamOf(p))); err != nil {
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
	srv := s.serverOf(p)
	if srv == nil {
		return gather.ErrNotInTeam.Error()
	}
	team, picked, ready, err := srv.g.Pick(p, name)
	if err != nil {
		if errors.Is(err, gather.ErrNoSuchMap) {
			return fmt.Sprintf("no map called %q in the pool; `%s` lists them.", name, s.cmd("maps"))
		}
		return err.Error()
	}
	if ready {
		s.stopPickTimer(srv)
		s.announce(fmt.Sprintf("%s: %s picks **%s**.", s.title(srv), teamTitle(team), picked))
		s.live(srv)
		return "picked."
	}
	return fmt.Sprintf("%s picks **%s**. Waiting for %s.", teamTitle(team), picked, teamTitle(1-team))
}

// Maps lists the pool.
func (s *Service) Maps() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "Map pool: " + strings.Join(s.servers[0].g.SortedPool(), ", ")
}

// Abort ends a gather: p's own, or the one on server name. admin says whether p may
// end a gather it isn't in, or clear the queue.
func (s *Service) Abort(p gather.Player, admin bool, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv := s.serverOf(p)
	if name != "" || srv == nil {
		if !admin {
			if srv == nil {
				return "only a player of a gather, or someone with Manage Server, can abort it."
			}
			return "you can only abort your own gather."
		}
		found, err := s.lookup(name)
		if err != nil {
			if len(s.active()) == 0 && len(s.queue) > 0 && name == "" {
				n := len(s.queue)
				s.queue = nil
				return fmt.Sprintf("the queue of %d is cleared.", n)
			}
			return err.Error() + fmt.Sprintf(" (`%s <server>`).", s.cmd("abort"))
		}
		srv = found
	}
	if srv.g.Phase == gather.Idle {
		if admin && len(s.queue) > 0 {
			n := len(s.queue)
			s.queue = nil
			return fmt.Sprintf("nothing is on at %s; the queue of %d is cleared.", srv.name, n)
		}
		return "nothing to abort."
	}
	title := s.title(srv)
	s.stopPickTimer(srv)
	srv.g.Reset()
	s.announce(fmt.Sprintf("%s aborted by %s. The server's password has been changed; `%s` to queue.", title, p.Name, s.cmd("add")))
	s.startWaiting(srv)
	return "aborted."
}

// Help lists the commands.
func (s *Service) Help() string {
	c := s.cmd
	lines := []string{
		fmt.Sprintf("`%s` — join the queue (%dv%d CTF)", c("add"), s.cfg.TeamSize, s.cfg.TeamSize),
		fmt.Sprintf("`%s` — leave the queue", c("del")),
		fmt.Sprintf("`%s` — the queue and every server", c("status")),
		fmt.Sprintf("`%s <map>` — your team's map, once your gather is full", c("pick")),
		fmt.Sprintf("`%s` — the map pool", c("maps")),
		fmt.Sprintf("`%s` — get your server info again", c("info")),
	}
	if len(s.servers) == 1 {
		lines = append(lines,
			fmt.Sprintf("`%s` — get the server info to spectate", c("spec")),
			fmt.Sprintf("`%s` — abort the running gather", c("abort")))
	} else {
		lines = append(lines,
			fmt.Sprintf("`%s [server]` — get the server info to spectate (servers: %s)", c("spec"), s.serverNames()),
			fmt.Sprintf("`%s [server]` — abort a running gather", c("abort")))
	}
	return strings.Join(lines, "\n")
}

// --- what the servers' scripts call -------------------------------------------------

// State is the API's view of server name's gather.
func (s *Service) State(name string) (api.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv, err := s.apiServer(name)
	if err != nil {
		return api.State{}, err
	}
	g := srv.g
	st := api.State{
		Server:       srv.name,
		GatherID:     g.ID,
		Phase:        g.Phase.String(),
		Password:     g.Password,
		SpecPassword: g.SpecPassword,
		Maps:         g.Maps(),
		Pool:         g.SortedPool(),
		Teams:        map[string][]string{},
	}
	if st.Maps == nil {
		st.Maps = []string{}
	}
	for t := range g.Teams {
		names := []string{}
		for _, p := range g.Teams[t] {
			names = append(names, p.Name)
		}
		st.Teams[gather.TeamName(t)] = names
	}
	return st, nil
}

// apiServer is the server a script means: by name, or the only one when it says none.
func (s *Service) apiServer(name string) (*server, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" && len(s.servers) == 1 {
		return s.servers[0], nil
	}
	if srv, ok := s.byName[name]; ok {
		return srv, nil
	}
	return nil, fmt.Errorf("%w %q", ErrNoServer, name)
}

// RoundEnd takes a round's report from server name: it is told in the channel, and
// the series ended when it is over.
func (s *Service) RoundEnd(name string, r gather.RoundReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv, err := s.apiServer(name)
	if err != nil {
		return err
	}
	done, err := srv.g.RoundEnd(r)
	if err != nil {
		return err
	}
	s.announce(s.roundText(srv, r))
	if done {
		s.finish(srv)
	}
	return nil
}

// ServerEvent notes who is on server name.
func (s *Service) ServerEvent(name string, ev api.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv, err := s.apiServer(name)
	if err != nil {
		return
	}
	switch ev.Type {
	case "join":
		srv.onServer[ev.Slot] = ev.Name
	case "leave":
		delete(srv.onServer, ev.Slot)
	}
}

// --- what the changes call for ----------------------------------------------------

// start takes a gather's worth of players off the queue and begins it on srv.
func (s *Service) start(srv *server) {
	players := s.queue[:s.size()]
	s.queue = append([]gather.Player(nil), s.queue[s.size():]...)
	if err := srv.g.Start(s.nextID, players, s.cfg.PickTimeout); err != nil {
		log.Printf("service: starting gather #%d on %s: %v", s.nextID, srv.name, err)
		return
	}
	s.nextID++
	s.started(srv)
}

// startWaiting begins a gather on srv, just freed, if the queue holds one.
func (s *Service) startWaiting(srv *server) {
	if len(s.queue) >= s.size() && srv.g.Phase == gather.Idle {
		s.announce(fmt.Sprintf("%s is free: the waiting gather starts there.", srv.name))
		s.start(srv)
	}
}

// started: the gather just began. Everyone is DMed the server, the teams are told,
// and the picking clock runs.
func (s *Service) started(srv *server) {
	g := srv.g
	var failed []string
	for t := range g.Teams {
		for _, p := range g.Teams[t] {
			if s.n == nil {
				continue
			}
			if err := s.n.DM(p.ID, s.playerDM(srv, t)); err != nil {
				log.Printf("service: DM to %s (%s): %v", p.Name, p.ID, err)
				failed = append(failed, p.Name)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s is full!**\n", s.title(srv))
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
	srv.pickTimer = time.AfterFunc(s.cfg.PickTimeout, func() { s.pickTimedOut(srv, id) })
}

func (s *Service) stopPickTimer(srv *server) {
	if srv.pickTimer != nil {
		srv.pickTimer.Stop()
		srv.pickTimer = nil
	}
}

// pickTimedOut: the clock ran out on gather id's picking on srv.
func (s *Service) pickTimedOut(srv *server, id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv.g.ID != id || srv.g.Phase != gather.Picking {
		return
	}
	forced, ready := srv.g.PickTimeout()
	if !ready {
		return
	}
	teams := make([]int, 0, len(forced))
	for t := range forced {
		teams = append(teams, t)
	}
	sort.Ints(teams)
	for _, t := range teams {
		s.announce(fmt.Sprintf("%s: time's up, %s gets **%s**.", s.title(srv), teamTitle(t), forced[t]))
	}
	s.live(srv)
}

// live: the maps are settled; the script picks them up on its next poll.
func (s *Service) live(srv *server) {
	spec := s.cmd("spec")
	if len(s.servers) > 1 {
		spec += " " + srv.name
	}
	s.announce(fmt.Sprintf("**%s is live!**\n%s\nServer: `%s` — say `/pw <your password>` in the chat once you join (it is in your DMs; `%s` sends it again). Watch with `%s`.",
		s.title(srv), mapsLine(srv.g), srv.addr, s.cmd("info"), spec))
}

// finish: the series on srv is over. The passwords change, so the server is locked,
// and a gather waiting in the queue starts there.
func (s *Service) finish(srv *server) {
	g := srv.g
	title := s.title(srv)
	var result string
	switch w := g.SeriesWinner(); w {
	case -1:
		result = fmt.Sprintf("a tie, %d - %d", g.Wins[gather.Alpha], g.Wins[gather.Bravo])
	default:
		result = fmt.Sprintf("**%s wins** %d - %d", teamTitle(w), g.Wins[w], g.Wins[1-w])
	}
	s.stopPickTimer(srv)
	g.Reset()
	s.announce(fmt.Sprintf("**%s is over:** %s. The server's password has been changed. `%s` to queue for the next one.", title, result, s.cmd("add")))
	s.startWaiting(srv)
}

// --- the texts -------------------------------------------------------------------------

func teamTitle(t int) string {
	if t == gather.Alpha {
		return "Alpha"
	}
	return "Bravo"
}

// playerDM is what a player of team t on srv is sent as the gather begins.
func (s *Service) playerDM(srv *server, t int) string {
	g := srv.g
	var b strings.Builder
	fmt.Fprintf(&b, "**%s is on!** You are on team **%s** with %s.\n", s.title(srv), teamTitle(t), gather.Names(g.Teams[t]))
	fmt.Fprintf(&b, "Server: `%s`\nPassword: `%s`\n", srv.addr, g.Password)
	fmt.Fprintf(&b, "Join the server, then say `/pw %s` in the chat (T) within %d seconds, or it kicks you. Then join team %s from the team menu (M).\n",
		g.Password, s.cfg.Grace, teamTitle(t))
	if g.Phase == gather.Picking {
		fmt.Fprintf(&b, "Your team picks its map in the channel: `%s <map>`.", s.cmd("pick"))
	} else {
		b.WriteString(mapsLine(g))
	}
	return b.String()
}

// mapsLine lists the series' maps, once live.
func mapsLine(g *gather.Gather) string {
	maps := g.Maps()
	if maps == nil {
		return "Maps: not picked yet."
	}
	return fmt.Sprintf("Maps: 1. **%s** (Alpha's pick) 2. **%s** (Bravo's pick) 3. **%s** (tiebreaker, if needed)", maps[0], maps[1], maps[2])
}

// roundText tells a round's end: the score, the series, and everyone's line.
func (s *Service) roundText(srv *server, r gather.RoundReport) string {
	g := srv.g
	var b strings.Builder
	fmt.Fprintf(&b, "**%s, map %d/%d — %s:** Alpha %d - %d Bravo", s.title(srv), r.MapIndex, len(g.Maps()), r.Map, r.Scores.Alpha, r.Scores.Bravo)
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

func onServerNames(srv *server) []string {
	names := make([]string, 0, len(srv.onServer))
	for _, n := range srv.onServer {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
