package gather

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

var pool = []string{"ctf_Ash", "ctf_Kampf", "ctf_Division", "ctf_Laos", "ctf_Run"}

func player(i int) Player { return Player{ID: fmt.Sprint(i), Name: fmt.Sprintf("p%d", i)} }

func six() []Player {
	var ps []Player
	for i := 0; i < 6; i++ {
		ps = append(ps, player(i))
	}
	return ps
}

func TestStartMakesTeams(t *testing.T) {
	g := New(3, pool, rand.New(rand.NewSource(1)))
	first := g.Password
	if err := g.Start(7, six()[:5], time.Minute); err != ErrWrongCount {
		t.Fatalf("five players: %v", err)
	}
	if err := g.Start(7, six(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if g.ID != 7 || g.Phase != Picking {
		t.Fatalf("id %d phase %v", g.ID, g.Phase)
	}
	if len(g.Teams[Alpha]) != 3 || len(g.Teams[Bravo]) != 3 {
		t.Fatalf("teams %v / %v", g.Teams[Alpha], g.Teams[Bravo])
	}
	if g.Password == first || g.Password == "" || g.Password == g.SpecPassword {
		t.Fatalf("passwords not renewed: %q %q %q", first, g.Password, g.SpecPassword)
	}
	seen := map[string]bool{}
	for _, p := range g.Members() {
		if seen[p.ID] {
			t.Fatalf("%s in two teams", p.ID)
		}
		seen[p.ID] = true
	}
	if g.TeamOf(player(9)) != -1 || g.TeamOf(player(0)) < 0 {
		t.Fatal("TeamOf")
	}
	if err := g.Start(8, six(), time.Minute); err != ErrNotIdle {
		t.Fatalf("start twice: %v", err)
	}
}

func TestPicksAndTiebreaker(t *testing.T) {
	g := New(3, pool, rand.New(rand.NewSource(2)))
	g.Start(1, six(), time.Minute)
	a, b := g.Teams[Alpha][0], g.Teams[Bravo][0]
	if _, _, _, err := g.Pick(player(9), "ash"); err != ErrNotInTeam {
		t.Fatalf("stranger's pick: %v", err)
	}
	if _, _, _, err := g.Pick(a, "nope"); err != ErrNoSuchMap {
		t.Fatalf("bad map: %v", err)
	}
	team, picked, ready, err := g.Pick(a, "ash")
	if err != nil || team != Alpha || picked != "ctf_Ash" || ready {
		t.Fatalf("alpha's pick: %d %q %v %v", team, picked, ready, err)
	}
	// a team may change its mind
	if _, picked, _, _ = g.Pick(a, "ctf_kampf"); picked != "ctf_Kampf" {
		t.Fatalf("repick: %q", picked)
	}
	team, picked, ready, err = g.Pick(b, "Div")
	if err != nil || team != Bravo || picked != "ctf_Division" || !ready {
		t.Fatalf("bravo's pick: %d %q %v %v", team, picked, ready, err)
	}
	if g.Phase != Live {
		t.Fatalf("phase %v", g.Phase)
	}
	maps := g.Maps()
	if len(maps) != 3 || maps[0] != "ctf_Kampf" || maps[1] != "ctf_Division" {
		t.Fatalf("maps %v", maps)
	}
	if maps[2] == maps[0] || maps[2] == maps[1] || maps[2] == "" {
		t.Fatalf("tiebreaker %q clashes", maps[2])
	}
	if _, _, _, err := g.Pick(a, "ash"); err != ErrNotPicking {
		t.Fatalf("pick while live: %v", err)
	}
}

func TestPickTimeoutFillsIn(t *testing.T) {
	g := New(3, pool, rand.New(rand.NewSource(3)))
	if forced, ready := g.PickTimeout(); forced != nil || ready {
		t.Fatal("timeout while idle did something")
	}
	g.Start(1, six(), time.Minute)
	g.Pick(g.Teams[Alpha][1], "laos")
	forced, ready := g.PickTimeout()
	if !ready || len(forced) != 1 || forced[Bravo] == "" || forced[Bravo] == "ctf_Laos" {
		t.Fatalf("forced %v ready %v", forced, ready)
	}
	if g.Phase != Live || g.Maps()[0] != "ctf_Laos" {
		t.Fatalf("phase %v maps %v", g.Phase, g.Maps())
	}
}

func TestResolveMap(t *testing.T) {
	g := New(3, []string{"ctf_Ash", "ctf_Amnesia", "ctf_Kampf"}, nil)
	cases := map[string]string{"ash": "ctf_Ash", "CTF_ASH": "ctf_Ash", "kam": "ctf_Kampf", "a": "", "": "", "x": ""}
	for in, want := range cases {
		got, ok := g.ResolveMap(in)
		if got != want || ok != (want != "") {
			t.Errorf("resolve %q = %q %v, want %q", in, got, ok, want)
		}
	}
}

func live(t *testing.T, seed int64) *Gather {
	t.Helper()
	g := New(3, pool, rand.New(rand.NewSource(seed)))
	g.Start(1, six(), time.Minute)
	g.Pick(g.Teams[Alpha][0], "ash")
	g.Pick(g.Teams[Bravo][0], "kampf")
	return g
}

func report(g *Gather, index int, winner string) RoundReport {
	return RoundReport{GatherID: g.ID, MapIndex: index, Map: g.Maps()[index-1], Winner: winner, Why: "limit"}
}

func TestSeriesTwoZero(t *testing.T) {
	g := live(t, 4)
	if _, err := g.RoundEnd(RoundReport{GatherID: 99}); err != ErrWrongID {
		t.Fatalf("wrong id: %v", err)
	}
	if done, err := g.RoundEnd(report(g, 1, "alpha")); err != nil || done {
		t.Fatalf("map 1: done=%v err=%v", done, err)
	}
	if done, err := g.RoundEnd(report(g, 2, "alpha")); err != nil || !done {
		t.Fatalf("map 2: done=%v err=%v", done, err)
	}
	if g.SeriesWinner() != Alpha || g.Wins != [2]int{2, 0} {
		t.Fatalf("winner %d wins %v", g.SeriesWinner(), g.Wins)
	}
}

func TestSeriesGoesToTiebreaker(t *testing.T) {
	g := live(t, 5)
	g.RoundEnd(report(g, 1, "alpha"))
	if done, _ := g.RoundEnd(report(g, 2, "bravo")); done {
		t.Fatal("1-1 ended the series")
	}
	if done, _ := g.RoundEnd(report(g, 3, "")); !done {
		t.Fatal("map 3 did not end the series")
	}
	if g.SeriesWinner() != -1 {
		t.Fatalf("winner %d, want a tie", g.SeriesWinner())
	}
}

func TestResetLocksServerAgain(t *testing.T) {
	g := live(t, 6)
	pw, spec := g.Password, g.SpecPassword
	g.Reset()
	if g.Phase != Idle || g.ID != 1 || len(g.Members()) != 0 || g.Maps() != nil {
		t.Fatalf("after reset: %+v", g)
	}
	if g.Password == pw || g.SpecPassword == spec {
		t.Fatal("passwords kept across the reset")
	}
	if _, err := g.RoundEnd(RoundReport{GatherID: g.ID, MapIndex: 1, Winner: "alpha"}); err != ErrNotLive {
		t.Fatalf("report while idle: %v", err)
	}
	if err := g.Start(2, six(), time.Minute); err != nil || g.ID != 2 {
		t.Fatalf("start after reset: %v, id %d", err, g.ID)
	}
}

func TestPasswordShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := NewPassword()
		if len(p) != 8 {
			t.Fatalf("password %q", p)
		}
		for _, c := range p {
			if c == '0' || c == 'o' || c == '1' || c == 'l' || c == 'i' {
				t.Fatalf("look-alike in %q", p)
			}
		}
		if seen[p] {
			t.Fatalf("repeat %q", p)
		}
		seen[p] = true
	}
}
