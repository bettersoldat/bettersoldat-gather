// Package gather holds the state of one gather on one server: the teams, the
// passwords, the tiebreaker and the series of rounds. The maps are picked in the game
// (the script's !map), so none of that is here. It knows nothing of Discord, of the
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
	// Live: the teams are made and the server plays their maps.
	Live
)

func (p Phase) String() string {
	switch p {
	case Idle:
		return "idle"
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

// RoundReport is a counted round's end, as the script posts it.
type RoundReport struct {
	GatherID int           `json:"gather_id"`
	MapIndex int           `json:"map_index"` // 1, 2 or 3: the tiebreaker
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
	Pool     []string // the maps !map takes, and the tiebreaker is drawn from

	Teams        [2][]Player // once started
	Tiebreaker   string      // drawn as the gather starts; played at 1-1 with !tb
	Password     string      // the server's password for the players
	SpecPassword string      // and for spectators
	Started      time.Time

	Results []RoundReport
	Wins    [2]int

	rng *mrand.Rand
}

// Errors the commands answer with.
var (
	ErrNotInTeam  = errors.New("you are not in this gather")
	ErrNotLive    = errors.New("no gather is live")
	ErrWrongID    = errors.New("the report is for another gather")
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
// the tiebreaker drawn, and live from now.
func (g *Gather) Start(id int, players []Player) error {
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
	g.Tiebreaker = g.randomMap()
	g.Results = nil
	g.Wins = [2]int{}
	g.Password = NewPassword()
	g.SpecPassword = NewPassword()
	g.Started = time.Now()
	g.Phase = Live
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

// randomMap is a map of the pool, or "" with no pool.
func (g *Gather) randomMap() string {
	if len(g.Pool) == 0 {
		return ""
	}
	return g.Pool[g.rng.Intn(len(g.Pool))]
}

// RoundEnd records a counted round of the live gather. done is true when the series
// is over: after the third map, after the second with a team ahead, or on the
// script's word.
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
	g.Tiebreaker = ""
	g.Results = nil
	g.Wins = [2]int{}
	g.Started = time.Time{}
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
