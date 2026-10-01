// Package gather holds the state of one gather on one server: the teams, the map
// picks, the passwords and the series of rounds. It knows nothing of Discord, of the
// queue or of the game server; the service around it turns its changes into messages.
package gather

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	mrand "math/rand"
	"sort"
	"strings"
	"time"
)

// Phase is where a gather stands.
type Phase int

const (
	// Idle: nothing is on; the server is locked and waits for the next gather.
	Idle Phase = iota
	// Picking: the teams are made and each picks its map.
	Picking
	// Live: the maps are settled and the server plays them.
	Live
)

func (p Phase) String() string {
	switch p {
	case Idle:
		return "idle"
	case Picking:
		return "picking"
	case Live:
		return "live"
	}
	return "unknown"
}

// Player is one Discord user: the ID is what it is known by, the name what it is called.
type Player struct {
	ID   string
	Name string
}

// Team indexes: Alpha is the first team, Bravo the second.
const (
	Alpha = 0
	Bravo = 1
)

// TeamName is the server's name for a team index.
func TeamName(t int) string {
	if t == Alpha {
		return "alpha"
	}
	return "bravo"
}

// PlayerStats is one player's line of a round, as the server's script reports it.
type PlayerStats struct {
	Name   string `json:"name"`
	Team   string `json:"team"`
	Kills  int    `json:"kills"`
	Deaths int    `json:"deaths"`
	Flags  int    `json:"flags"`
	Ping   int    `json:"ping"`
}

// RoundReport is a round's end, as the script posts it.
type RoundReport struct {
	GatherID int           `json:"gather_id"`
	MapIndex int           `json:"map_index"` // 1-based, into Maps()
	Map      string        `json:"map"`
	Why      string        `json:"why"` // "limit", "nextmap" or "vote"
	Scores   Scores        `json:"scores"`
	Winner   string        `json:"winner"` // "alpha", "bravo" or ""
	Players  []PlayerStats `json:"players"`
	Done     bool          `json:"done"` // the script's word that the series is over
}

// Scores is a round's score by team.
type Scores struct {
	Alpha int `json:"alpha"`
	Bravo int `json:"bravo"`
}

// Gather is the state. It is not safe for concurrent use; the service locks around it.
type Gather struct {
	ID       int // the gather's number; 0 before the first, and the last one's while idle
	Phase    Phase
	TeamSize int
	Pool     []string // the maps a team may pick

	Teams        [2][]Player // once started
	Picks        [2]string   // each team's map, "" until picked
	Tiebreaker   string      // chosen as the gather goes live
	Password     string      // the server's password for the players
	SpecPassword string      // and for spectators
	PickDeadline time.Time

	Results []RoundReport
	Wins    [2]int

	rng *mrand.Rand
}

// Errors the commands answer with.
var (
	ErrNotPicking = errors.New("there is no map to pick right now")
	ErrNotInTeam  = errors.New("you are not in this gather")
	ErrNotLive    = errors.New("no gather is live")
	ErrWrongID    = errors.New("the report is for another gather")
	ErrNoSuchMap  = errors.New("no such map in the pool")
	ErrNotIdle    = errors.New("a gather is already on")
	ErrWrongCount = errors.New("not the number of players a gather takes")
)

// New makes an idle gather, with passwords already set so the server is locked from
// the start. rng may be nil for a time-seeded one.
func New(teamSize int, pool []string, rng *mrand.Rand) *Gather {
	if rng == nil {
		rng = mrand.New(mrand.NewSource(time.Now().UnixNano()))
	}
	g := &Gather{TeamSize: teamSize, Pool: append([]string(nil), pool...), rng: rng}
	g.Password = NewPassword()
	g.SpecPassword = NewPassword()
	return g
}

// Size is how many players a gather takes.
func (g *Gather) Size() int { return 2 * g.TeamSize }

// Start begins gather id with these players: shuffled into two teams, new passwords,
// and the picking begun with pickTimeout to go.
func (g *Gather) Start(id int, players []Player, pickTimeout time.Duration) error {
	if g.Phase != Idle {
		return ErrNotIdle
	}
	if len(players) != g.Size() {
		return ErrWrongCount
	}
	players = append([]Player(nil), players...)
	g.rng.Shuffle(len(players), func(i, j int) { players[i], players[j] = players[j], players[i] })
	g.ID = id
	g.Teams[Alpha] = players[:g.TeamSize]
	g.Teams[Bravo] = players[g.TeamSize:]
	g.Picks = [2]string{}
	g.Tiebreaker = ""
	g.Results = nil
	g.Wins = [2]int{}
	g.Password = NewPassword()
	g.SpecPassword = NewPassword()
	g.PickDeadline = time.Now().Add(pickTimeout)
	g.Phase = Picking
	return nil
}

// TeamOf is p's team index, or -1 when p is not in the gather's teams.
func (g *Gather) TeamOf(p Player) int {
	for t := range g.Teams {
		for _, q := range g.Teams[t] {
			if q.ID == p.ID {
				return t
			}
		}
	}
	return -1
}

// Members is everyone in the teams, alpha first.
func (g *Gather) Members() []Player {
	return append(append([]Player(nil), g.Teams[Alpha]...), g.Teams[Bravo]...)
}

// ResolveMap finds name in the pool: exact, case-insensitive, with or without the
// "ctf_" prefix, or as a unique prefix of a map's name.
func (g *Gather) ResolveMap(name string) (string, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return "", false
	}
	bare := strings.TrimPrefix(want, "ctf_")
	var prefixed []string
	for _, m := range g.Pool {
		low := strings.ToLower(m)
		if low == want || strings.TrimPrefix(low, "ctf_") == bare {
			return m, true
		}
		if strings.HasPrefix(strings.TrimPrefix(low, "ctf_"), bare) {
			prefixed = append(prefixed, m)
		}
	}
	if len(prefixed) == 1 {
		return prefixed[0], true
	}
	return "", false
}

// Pick records p's team's map. ready is true once both teams have picked: the gather
// is then live, its tiebreaker chosen. A team may change its pick until then.
func (g *Gather) Pick(p Player, name string) (team int, picked string, ready bool, err error) {
	if g.Phase != Picking {
		return -1, "", false, ErrNotPicking
	}
	team = g.TeamOf(p)
	if team < 0 {
		return -1, "", false, ErrNotInTeam
	}
	picked, ok := g.ResolveMap(name)
	if !ok {
		return team, "", false, ErrNoSuchMap
	}
	g.Picks[team] = picked
	if g.Picks[Alpha] != "" && g.Picks[Bravo] != "" {
		g.goLive()
		return team, picked, true, nil
	}
	return team, picked, false, nil
}

// PickTimeout picks at random for any team that hasn't, and goes live. It answers
// which teams were picked for. Nothing happens unless the gather is picking.
func (g *Gather) PickTimeout() (forced map[int]string, ready bool) {
	if g.Phase != Picking {
		return nil, false
	}
	forced = map[int]string{}
	for t := range g.Picks {
		if g.Picks[t] == "" {
			g.Picks[t] = g.randomMap(g.Picks[Alpha], g.Picks[Bravo])
			forced[t] = g.Picks[t]
		}
	}
	g.goLive()
	return forced, true
}

// goLive: the tiebreaker chosen apart from the picks, and the phase set.
func (g *Gather) goLive() {
	g.Tiebreaker = g.randomMap(g.Picks[Alpha], g.Picks[Bravo])
	g.Phase = Live
}

// randomMap is a map of the pool that is none of `not` (falling back to any of the pool
// when it is too small, and to the first pick when the pool is empty).
func (g *Gather) randomMap(not ...string) string {
	var choices []string
	for _, m := range g.Pool {
		excluded := false
		for _, n := range not {
			if strings.EqualFold(m, n) {
				excluded = true
			}
		}
		if !excluded {
			choices = append(choices, m)
		}
	}
	if len(choices) == 0 {
		choices = g.Pool
	}
	if len(choices) == 0 {
		for _, n := range not {
			if n != "" {
				return n
			}
		}
		return ""
	}
	return choices[g.rng.Intn(len(choices))]
}

// Maps is the series in order: alpha's pick, bravo's, the tiebreaker. Empty until live.
func (g *Gather) Maps() []string {
	if g.Phase != Live {
		return nil
	}
	return []string{g.Picks[Alpha], g.Picks[Bravo], g.Tiebreaker}
}

// RoundEnd records a round of the live gather. done is true when the series is over:
// after the third map, after the second with a team ahead, or on the script's word.
func (g *Gather) RoundEnd(r RoundReport) (done bool, err error) {
	if g.Phase != Live {
		return false, ErrNotLive
	}
	if r.GatherID != g.ID {
		return false, ErrWrongID
	}
	g.Results = append(g.Results, r)
	switch r.Winner {
	case "alpha":
		g.Wins[Alpha]++
	case "bravo":
		g.Wins[Bravo]++
	}
	if r.Done || r.MapIndex >= 3 {
		return true, nil
	}
	if r.MapIndex == 2 && g.Wins[Alpha] != g.Wins[Bravo] {
		return true, nil
	}
	return false, nil
}

// SeriesWinner is the team ahead on maps, or -1 for a tie.
func (g *Gather) SeriesWinner() int {
	switch {
	case g.Wins[Alpha] > g.Wins[Bravo]:
		return Alpha
	case g.Wins[Bravo] > g.Wins[Alpha]:
		return Bravo
	}
	return -1
}

// Reset ends the gather, whatever its phase: idle again, with new passwords so the
// server is locked. The ID stays, as the last gather's number.
func (g *Gather) Reset() {
	g.Phase = Idle
	g.Teams = [2][]Player{}
	g.Picks = [2]string{}
	g.Tiebreaker = ""
	g.Results = nil
	g.Wins = [2]int{}
	g.PickDeadline = time.Time{}
	g.Password = NewPassword()
	g.SpecPassword = NewPassword()
}

// SortedPool is the pool in alphabetical order, for listing.
func (g *Gather) SortedPool() []string {
	out := append([]string(nil), g.Pool...)
	sort.Strings(out)
	return out
}

// Names of a list of players, comma-separated.
func Names(ps []Player) string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

const passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/l/i

// NewPassword is 8 random characters, from crypto/rand, with no look-alike letters.
func NewPassword() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			panic(fmt.Sprintf("crypto/rand: %v", err))
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String()
}
