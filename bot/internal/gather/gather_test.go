package gather

import (
	"fmt"
	"math/rand"
	"testing"
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

func TestStartMakesTeamsAndIsLive(t *testing.T) {
	g := New(3, pool, rand.New(rand.NewSource(1)))
	first := g.Password
	if err := g.Start(7, six()[:5]); err != ErrWrongCount {
		t.Fatalf("five players: %v", err)
	}
	if err := g.Start(7, six()); err != nil {
		t.Fatal(err)
	}
	if g.ID != 7 || g.Phase != Live {
		t.Fatalf("id %d phase %v", g.ID, g.Phase)
	}
	if len(g.Teams[Alpha]) != 3 || len(g.Teams[Bravo]) != 3 {
		t.Fatalf("teams %v / %v", g.Teams[Alpha], g.Teams[Bravo])
	}
	if g.Password == first || g.Password == "" || g.Password == g.SpecPassword {
		t.Fatalf("passwords not renewed: %q %q %q", first, g.Password, g.SpecPassword)
	}
	inPool := false
	for _, m := range pool {
		if m == g.Tiebreaker {
			inPool = true
		}
	}
	if !inPool {
		t.Fatalf("tiebreaker %q is not of the pool", g.Tiebreaker)
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
	if err := g.Start(8, six()); err != ErrNotIdle {
		t.Fatalf("start twice: %v", err)
	}
}

func live(t *testing.T, seed int64) *Gather {
	t.Helper()
	g := New(3, pool, rand.New(rand.NewSource(seed)))
	g.Start(1, six())
	return g
}

func report(g *Gather, index int, winner string) RoundReport {
	return RoundReport{GatherID: g.ID, MapIndex: index, Map: "ctf_Ash", Winner: winner, Why: "limit"}
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
	if g.Phase != Idle || g.ID != 1 || len(g.Members()) != 0 || g.Tiebreaker != "" {
		t.Fatalf("after reset: %+v", g)
	}
	if g.Password == pw || g.SpecPassword == spec {
		t.Fatal("passwords kept across the reset")
	}
	if _, err := g.RoundEnd(report(g, 1, "alpha")); err != ErrNotLive {
		t.Fatalf("report while idle: %v", err)
	}
	if err := g.Start(2, six()); err != nil || g.ID != 2 {
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
