package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gatherbot/internal/api"
	"gatherbot/internal/gather"
)

type fakeNotifier struct {
	mu        sync.Mutex
	announced []string
	dms       map[string][]string
	failDM    map[string]bool
}

func newNotifier() *fakeNotifier {
	return &fakeNotifier{dms: map[string][]string{}, failDM: map[string]bool{}}
}

func (n *fakeNotifier) Announce(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.announced = append(n.announced, text)
}

func (n *fakeNotifier) DM(userID, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failDM[userID] {
		return fmt.Errorf("closed")
	}
	n.dms[userID] = append(n.dms[userID], text)
	return nil
}

func (n *fakeNotifier) last() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.announced) == 0 {
		return ""
	}
	return n.announced[len(n.announced)-1]
}

func (n *fakeNotifier) all() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.announced, "\n---\n")
}

var pool = []string{"ctf_Ash", "ctf_Kampf", "ctf_Division", "ctf_Laos"}

func newService(n Notifier, pickTimeout time.Duration) *Service {
	return New(Config{ServerAddr: "game.example:23073", TeamSize: 3, Pool: pool, PickTimeout: pickTimeout, Prefix: "!beta_", Grace: 30}, n)
}

func player(i int) gather.Player {
	return gather.Player{ID: fmt.Sprint(i), Name: fmt.Sprintf("p%d", i)}
}

func fill(t *testing.T, s *Service) {
	t.Helper()
	for i := 0; i < 6; i++ {
		reply := s.Add(player(i))
		if i < 5 && !strings.Contains(reply, fmt.Sprintf("(%d/6)", i+1)) {
			t.Fatalf("add %d: %q", i, reply)
		}
	}
}

func TestFillDMsEveryoneAndAnnounces(t *testing.T) {
	n := newNotifier()
	n.failDM["4"] = true
	s := newService(n, time.Hour)
	fill(t, s)
	st := s.State()
	if st.Phase != "picking" || len(st.Teams["alpha"]) != 3 || len(st.Teams["bravo"]) != 3 || len(st.Maps) != 0 {
		t.Fatalf("state %+v", st)
	}
	for i := 0; i < 6; i++ {
		id := fmt.Sprint(i)
		dm := n.dms[id]
		if i == 4 {
			if len(dm) != 0 {
				t.Fatalf("DM to the closed one: %v", dm)
			}
			continue
		}
		if len(dm) != 1 || !strings.Contains(dm[0], st.Password) || !strings.Contains(dm[0], "game.example:23073") || !strings.Contains(dm[0], "/pw "+st.Password) {
			t.Fatalf("DM to %s: %v", id, dm)
		}
	}
	last := n.last()
	if !strings.Contains(last, "Gather #1 is full") || !strings.Contains(last, "couldn't DM p4") || !strings.Contains(last, "!beta_pick") {
		t.Fatalf("announcement: %q", last)
	}
	if strings.Contains(last, st.Password) {
		t.Fatal("the password was announced in the channel")
	}
	if reply := s.Info(player(4)); !strings.Contains(reply, "couldn't DM") || len(n.dms["4"]) != 0 {
		// still closed
		t.Fatalf("info with closed DMs: %q %v", reply, n.dms["4"])
	}
	n.failDM["4"] = false
	if reply := s.Info(player(4)); !strings.Contains(reply, "DMs") || len(n.dms["4"]) != 1 {
		t.Fatalf("info: %q %v", reply, n.dms["4"])
	}
}

func TestPicksGoLiveAndSeriesEnds(t *testing.T) {
	n := newNotifier()
	s := newService(n, time.Hour)
	fill(t, s)
	st := s.State()
	alpha := gather.Player{ID: idOf(t, s, "alpha", 0)}
	bravo := gather.Player{ID: idOf(t, s, "bravo", 0)}
	if reply := s.Pick(alpha, ""); !strings.Contains(reply, "which map") {
		t.Fatalf("empty pick: %q", reply)
	}
	if reply := s.Pick(alpha, "zzz"); !strings.Contains(reply, "no map called") {
		t.Fatalf("bad pick: %q", reply)
	}
	if reply := s.Pick(alpha, "ash"); !strings.Contains(reply, "Alpha picks **ctf_Ash**") {
		t.Fatalf("alpha's pick: %q", reply)
	}
	if reply := s.Pick(bravo, "kampf"); reply != "picked." {
		t.Fatalf("bravo's pick: %q", reply)
	}
	st = s.State()
	if st.Phase != "live" || len(st.Maps) != 3 || st.Maps[0] != "ctf_Ash" || st.Maps[1] != "ctf_Kampf" {
		t.Fatalf("state %+v", st)
	}
	if !strings.Contains(n.last(), "Gather #1 is live") || !strings.Contains(n.last(), st.Maps[2]) {
		t.Fatalf("live announcement: %q", n.last())
	}
	if reply := s.Spec(player(9)); reply != "check your DMs." || !strings.Contains(n.dms["9"][0], st.SpecPassword) {
		t.Fatalf("spec: %q %v", reply, n.dms["9"])
	}

	report := func(index int, winner string, a, b int) error {
		return s.RoundEnd(gather.RoundReport{GatherID: 1, MapIndex: index, Map: st.Maps[index-1], Why: "limit", Winner: winner,
			Scores: gather.Scores{Alpha: a, Bravo: b}, Players: []gather.PlayerStats{{Name: "p0", Team: "alpha", Kills: 7, Deaths: 3, Flags: a}}})
	}
	if err := report(1, "alpha", 5, 2); err != nil {
		t.Fatal(err)
	}
	if last := n.last(); !strings.Contains(last, "map 1/3") || !strings.Contains(last, "Alpha 5 - 2 Bravo") || !strings.Contains(last, "Series: Alpha 1 - 0") || !strings.Contains(last, "p0") {
		t.Fatalf("round text: %q", last)
	}
	if s.State().Phase != "live" {
		t.Fatal("ended after one map")
	}
	if err := report(2, "bravo", 1, 4); err != nil {
		t.Fatal(err)
	}
	if s.State().Phase != "live" {
		t.Fatal("ended at 1-1")
	}
	if err := report(3, "bravo", 0, 1); err != nil {
		t.Fatal(err)
	}
	after := s.State()
	if after.Phase != "idle" || after.GatherID != 2 || after.Password == st.Password || after.SpecPassword == st.SpecPassword {
		t.Fatalf("after the series: %+v", after)
	}
	if !strings.Contains(n.last(), "Gather #1 is over") || !strings.Contains(n.last(), "Bravo wins** 2 - 1") {
		t.Fatalf("final: %q", n.last())
	}
	if err := report(1, "alpha", 1, 0); err == nil {
		t.Fatal("a report after the end was taken")
	}
}

func TestPickTimeoutPicksForTheSlow(t *testing.T) {
	n := newNotifier()
	s := newService(n, 50*time.Millisecond)
	fill(t, s)
	s.Pick(gather.Player{ID: idOf(t, s, "alpha", 1)}, "laos")
	deadline := time.Now().Add(2 * time.Second)
	for s.State().Phase != "live" {
		if time.Now().After(deadline) {
			t.Fatalf("never went live: %s", n.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := s.State()
	if st.Maps[0] != "ctf_Laos" || st.Maps[1] == "" || st.Maps[1] == "ctf_Laos" {
		t.Fatalf("maps %v", st.Maps)
	}
	if !strings.Contains(n.all(), "Time's up: Bravo gets") {
		t.Fatalf("announcements: %s", n.all())
	}
}

func TestAbortAndStatus(t *testing.T) {
	n := newNotifier()
	s := newService(n, time.Hour)
	if reply := s.Abort(player(1), true); reply != "nothing to abort." {
		t.Fatalf("abort idle: %q", reply)
	}
	fill(t, s)
	if reply := s.Abort(player(9), false); !strings.Contains(reply, "only a player") {
		t.Fatalf("stranger's abort: %q", reply)
	}
	s.ServerEvent(api.Event{Type: "join", Slot: 0, Name: "Major"})
	s.ServerEvent(api.Event{Type: "join", Slot: 1, Name: "Minor"})
	s.ServerEvent(api.Event{Type: "leave", Slot: 1, Name: "Minor"})
	status := s.Status()
	if !strings.Contains(status, "picking maps") || !strings.Contains(status, "On the server now: Major") || strings.Contains(status, "Minor") {
		t.Fatalf("status: %q", status)
	}
	pw := s.State().Password
	if reply := s.Abort(player(0), false); reply != "aborted." {
		t.Fatalf("abort: %q", reply)
	}
	st := s.State()
	if st.Phase != "idle" || st.GatherID != 2 || st.Password == pw {
		t.Fatalf("after abort: %+v", st)
	}
	if !strings.Contains(n.last(), "Gather #1 aborted by p0") {
		t.Fatalf("abort announcement: %q", n.last())
	}
	if reply := s.Del(player(0)); reply != gather.ErrNotInQueue.Error() {
		t.Fatalf("del after abort: %q", reply)
	}
	if !strings.Contains(s.Status(), "waiting for players (0/6)") {
		t.Fatalf("idle status: %q", s.Status())
	}
	if !strings.Contains(s.Maps(), "ctf_Ash, ctf_Division") || !strings.Contains(s.Help(), "!beta_add") {
		t.Fatalf("maps/help: %q / %q", s.Maps(), s.Help())
	}
}

// idOf is the ID of the i-th player of team name, read from the state's names.
func idOf(t *testing.T, s *Service, team string, i int) string {
	t.Helper()
	name := s.State().Teams[team][i]
	return strings.TrimPrefix(name, "p")
}
