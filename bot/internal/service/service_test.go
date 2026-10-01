package service

import (
	"context"
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

func newService(n Notifier, servers ...ServerConfig) *Service {
	if len(servers) == 0 {
		servers = []ServerConfig{{Name: "main", Addr: "game.example:23073"}}
	}
	return New(Config{Servers: servers, TeamSize: 3, Pool: pool, Prefix: "!beta_"}, n)
}

func player(i int) gather.Player {
	return gather.Player{ID: fmt.Sprint(i), Name: fmt.Sprintf("p%d", i)}
}

// fill adds players from..from+5; the last add's reply is returned.
func fill(t *testing.T, s *Service, from int) string {
	t.Helper()
	var reply string
	for i := from; i < from+6; i++ {
		reply = s.Add(player(i))
		if i < from+5 && !strings.Contains(reply, fmt.Sprintf("(%d/6)", i-from+1)) {
			t.Fatalf("add %d: %q", i, reply)
		}
	}
	return reply
}

func state(t *testing.T, s *Service, server string) api.State {
	t.Helper()
	st, err := s.State(server)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestFillGoesLiveDMsEveryoneAndAnnounces(t *testing.T) {
	n := newNotifier()
	n.failDM["4"] = true
	s := newService(n)
	if reply := fill(t, s, 0); !strings.Contains(reply, "gather #1 is on") {
		t.Fatalf("last add: %q", reply)
	}
	st := state(t, s, "")
	if st.Server != "main" || st.GatherID != 1 || st.Phase != "live" || len(st.Teams["alpha"]) != 3 || len(st.Teams["bravo"]) != 3 || st.Tiebreaker == "" || len(st.Pool) != 4 {
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
		if len(dm) != 1 || !strings.Contains(dm[0], "Password: `"+st.Password+"`") || !strings.Contains(dm[0], "game.example:23073") || !strings.Contains(dm[0], "!map") || !strings.Contains(dm[0], st.Tiebreaker) {
			t.Fatalf("DM to %s: %v", id, dm)
		}
	}
	last := n.last()
	if !strings.Contains(last, "Gather #1 is on") || !strings.Contains(last, "couldn't DM p4") || !strings.Contains(last, "!map") || !strings.Contains(last, "!tb") || !strings.Contains(last, st.Tiebreaker) {
		t.Fatalf("announcement: %q", last)
	}
	if strings.Contains(last, st.Password) {
		t.Fatal("the password was announced in the channel")
	}
	if reply := s.Info(player(4)); !strings.Contains(reply, "couldn't DM") || len(n.dms["4"]) != 0 {
		t.Fatalf("info with closed DMs: %q %v", reply, n.dms["4"])
	}
	n.failDM["4"] = false
	if reply := s.Info(player(4)); !strings.Contains(reply, "DMs") || len(n.dms["4"]) != 1 {
		t.Fatalf("info: %q %v", reply, n.dms["4"])
	}
	if reply := s.Add(player(0)); !strings.Contains(reply, "already playing in gather #1") {
		t.Fatalf("add while playing: %q", reply)
	}
	if reply := s.Add(player(7)); !strings.Contains(reply, "(1/6)") {
		t.Fatalf("the queue did not go on: %q", reply)
	}
	if reply := s.Add(player(7)); reply != ErrInQueue.Error() {
		t.Fatalf("double add: %q", reply)
	}
	if reply := s.Del(player(7)); !strings.Contains(reply, "(0/6)") {
		t.Fatalf("del: %q", reply)
	}
	if reply := s.Del(player(7)); reply != ErrNotInQueue.Error() {
		t.Fatalf("del again: %q", reply)
	}
	if reply := s.Del(player(0)); !strings.Contains(reply, "you are playing") {
		t.Fatalf("del while playing: %q", reply)
	}
	if reply := s.Spec(player(9), ""); reply != "check your DMs." || !strings.Contains(n.dms["9"][0], st.Password) {
		t.Fatalf("spec: %q %v", reply, n.dms["9"])
	}
}

func TestSeriesEnds(t *testing.T) {
	n := newNotifier()
	s := newService(n)
	fill(t, s, 0)
	st := state(t, s, "")
	report := func(index int, m, winner string, a, b int) error {
		return s.RoundEnd("", gather.RoundReport{GatherID: 1, MapIndex: index, Map: m, Why: "limit", Winner: winner,
			Scores: gather.Scores{Alpha: a, Bravo: b}, Players: []gather.PlayerStats{{Name: "p0", Team: "alpha", Kills: 7, Deaths: 3, Flags: a}}})
	}
	if err := report(1, "ctf_Ash", "alpha", 5, 2); err != nil {
		t.Fatal(err)
	}
	if last := n.last(); !strings.Contains(last, "map 1/3 — ctf_Ash") || !strings.Contains(last, "Alpha 5 - 2 Bravo") || !strings.Contains(last, "Series: Alpha 1 - 0") || !strings.Contains(last, "p0") {
		t.Fatalf("round text: %q", last)
	}
	if state(t, s, "").Phase != "live" {
		t.Fatal("ended after one map")
	}
	if !strings.Contains(s.Status(), "1 of 3 maps played") || !strings.Contains(s.Status(), "1. ctf_Ash 5 - 2") {
		t.Fatalf("status: %q", s.Status())
	}
	if err := report(2, "ctf_Kampf", "bravo", 1, 4); err != nil {
		t.Fatal(err)
	}
	if state(t, s, "").Phase != "live" {
		t.Fatal("ended at 1-1")
	}
	if err := report(3, st.Tiebreaker, "bravo", 0, 1); err != nil {
		t.Fatal(err)
	}
	after := state(t, s, "")
	if after.Phase != "idle" || after.Password == st.Password || after.Tiebreaker != "" {
		t.Fatalf("after the series: %+v", after)
	}
	if !strings.Contains(n.last(), "Gather #1 is over") || !strings.Contains(n.last(), "Bravo wins** 2 - 1") {
		t.Fatalf("final: %q", n.last())
	}
	if err := report(1, "ctf_Ash", "alpha", 1, 0); err == nil {
		t.Fatal("a report after the end was taken")
	}
	if reply := s.Spec(player(9), ""); !strings.Contains(reply, "no gather to watch") {
		t.Fatalf("spec while idle: %q", reply)
	}
	fill(t, s, 10)
	if state(t, s, "").GatherID != 2 {
		t.Fatal("the next gather is not #2")
	}
}

func TestAbortAndStatus(t *testing.T) {
	n := newNotifier()
	s := newService(n)
	if reply := s.Abort(player(1), true, ""); reply != "nothing to abort." {
		t.Fatalf("abort idle: %q", reply)
	}
	fill(t, s, 0)
	if reply := s.Abort(player(9), false, ""); !strings.Contains(reply, "only a player") {
		t.Fatalf("stranger's abort: %q", reply)
	}
	s.ServerEvent("", api.Event{Type: "join", Slot: 0, Name: "Major"})
	s.ServerEvent("", api.Event{Type: "join", Slot: 1, Name: "Minor"})
	s.ServerEvent("", api.Event{Type: "leave", Slot: 1, Name: "Minor"})
	s.ServerEvent("nowhere", api.Event{Type: "join", Slot: 2, Name: "Ghost"})
	status := s.Status()
	if !strings.Contains(status, "gather #1, live") || !strings.Contains(status, "On the server now: Major") || strings.Contains(status, "Minor") || strings.Contains(status, "Ghost") {
		t.Fatalf("status: %q", status)
	}
	pw := state(t, s, "").Password
	if reply := s.Abort(player(0), false, ""); reply != "aborted." {
		t.Fatalf("abort: %q", reply)
	}
	st := state(t, s, "")
	if st.Phase != "idle" || st.Password == pw {
		t.Fatalf("after abort: %+v", st)
	}
	if !strings.Contains(n.last(), "Gather #1 aborted by p0") {
		t.Fatalf("abort announcement: %q", n.last())
	}
	if !strings.Contains(s.Status(), "**Queue** 0/6") || !strings.Contains(s.Status(), "free") {
		t.Fatalf("idle status: %q", s.Status())
	}
	if !strings.Contains(s.Maps(), "ctf_Ash, ctf_Division") || !strings.Contains(s.Help(), "!beta_add") || !strings.Contains(s.Help(), "!tb") {
		t.Fatalf("maps/help: %q / %q", s.Maps(), s.Help())
	}
	s.Add(player(1))
	if reply := s.Abort(player(1), true, ""); !strings.Contains(reply, "queue of 1 is cleared") || len(s.queue) != 0 {
		t.Fatalf("admin clears the queue: %q", reply)
	}
}

func TestTwoServersPlayAtOnce(t *testing.T) {
	n := newNotifier()
	s := newService(n, ServerConfig{Name: "eu1", Addr: "eu.example:23073"}, ServerConfig{Name: "na1", Addr: "na.example:23073"})
	if reply := fill(t, s, 0); !strings.Contains(reply, "gather #1 on eu1 is on") {
		t.Fatalf("first fill: %q", reply)
	}
	if reply := fill(t, s, 10); !strings.Contains(reply, "gather #2 on na1 is on") {
		t.Fatalf("second fill: %q", reply)
	}
	if !strings.Contains(n.dms["10"][0], "na.example:23073") || !strings.Contains(n.dms["0"][0], "eu.example:23073") {
		t.Fatalf("DMs name the wrong server: %q / %q", n.dms["10"][0], n.dms["0"][0])
	}
	if reply := fill(t, s, 20); !strings.Contains(reply, "waiting for a free server") {
		t.Fatalf("third fill: %q", reply)
	}
	if !strings.Contains(n.last(), "every server is busy") {
		t.Fatalf("busy announcement: %q", n.last())
	}
	if _, err := s.State(""); err == nil {
		t.Fatal("a state with no server name was served")
	}
	if _, err := s.State("xx"); err == nil {
		t.Fatal("a state for an unknown server was served")
	}
	eu, na := state(t, s, "eu1"), state(t, s, "NA1")
	if eu.GatherID != 1 || na.GatherID != 2 || eu.Password == na.Password {
		t.Fatalf("states %+v / %+v", eu, na)
	}
	if reply := s.Spec(player(99), ""); !strings.Contains(reply, "which server") {
		t.Fatalf("spec without a name: %q", reply)
	}
	if reply := s.Spec(player(99), "na1"); reply != "check your DMs." || !strings.Contains(n.dms["99"][0], na.Password) {
		t.Fatalf("spec na1: %q", reply)
	}
	status := s.Status()
	if !strings.Contains(status, "**Queue** 6/6") || !strings.Contains(status, "**eu1**") || !strings.Contains(status, "**na1**") {
		t.Fatalf("status: %q", status)
	}

	// na1's gather is won 2-0; the waiting gather starts there
	for i := 1; i <= 2; i++ {
		if err := s.RoundEnd("na1", gather.RoundReport{GatherID: 2, MapIndex: i, Map: "ctf_Ash", Winner: "alpha", Why: "limit"}); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(n.all(), "Gather #2 on na1 is over") || !strings.Contains(n.all(), "na1 is free: the waiting gather starts there") {
		t.Fatalf("announcements: %s", n.all())
	}
	na = state(t, s, "na1")
	if na.GatherID != 3 || na.Phase != "live" || len(s.queue) != 0 {
		t.Fatalf("na1 after: %+v, queue %v", na, s.queue)
	}
	if s.serverOf(player(20)) == nil || s.serverOf(player(20)).name != "na1" {
		t.Fatal("the waiting players are not on na1")
	}
	// eu1's is still on; an admin aborts it by name
	if reply := s.Abort(player(99), true, "eu1"); reply != "aborted." {
		t.Fatalf("abort eu1: %q", reply)
	}
	if state(t, s, "eu1").Phase != "idle" || state(t, s, "na1").Phase != "live" {
		t.Fatal("the wrong gather was aborted")
	}
	if reply := s.Abort(player(20), false, "eu1"); !strings.Contains(reply, "your own gather") {
		t.Fatalf("a player aborting another server's: %q", reply)
	}
}

func TestWaitStateAnswersOnChange(t *testing.T) {
	s := newService(newNotifier())
	st := state(t, s, "")
	if st.Version != 1 {
		t.Fatalf("first version %d", st.Version)
	}
	// nothing changes: the wait ends with the context, same version
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if got, err := s.WaitState(ctx, "", st.Version); err != nil || got.Version != st.Version {
		t.Fatalf("wait with nothing: %+v %v", got, err)
	}
	// a gather starting is a change: the wait returns with the new password
	done := make(chan api.State, 1)
	go func() {
		got, _ := s.WaitState(context.Background(), "", st.Version)
		done <- got
	}()
	time.Sleep(20 * time.Millisecond)
	fill(t, s, 0)
	select {
	case got := <-done:
		if got.Version != st.Version+1 || got.Phase != "live" || got.Password == st.Password {
			t.Fatalf("after the change: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait never returned")
	}
	// an older version is answered at once
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	start := time.Now()
	if got, _ := s.WaitState(ctx2, "", 0); got.Version != st.Version+1 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("stale since: %+v after %s", got, time.Since(start))
	}
	if _, err := s.WaitState(ctx2, "nowhere", 0); err == nil {
		t.Fatal("a wait on an unknown server was served")
	}
	// the end of the series is a change too
	s.RoundEnd("", gather.RoundReport{GatherID: 1, MapIndex: 1, Map: "ctf_Ash", Winner: "alpha"})
	s.RoundEnd("", gather.RoundReport{GatherID: 1, MapIndex: 2, Map: "ctf_Ash", Winner: "alpha"})
	if state(t, s, "").Version != st.Version+2 {
		t.Fatalf("version after the series: %d", state(t, s, "").Version)
	}
}

func TestRoundReportForTheWrongServer(t *testing.T) {
	s := newService(newNotifier(), ServerConfig{Name: "eu1", Addr: "a"}, ServerConfig{Name: "na1", Addr: "b"})
	fill(t, s, 0)
	if err := s.RoundEnd("na1", gather.RoundReport{GatherID: 1, MapIndex: 1}); err != gather.ErrNotLive {
		t.Fatalf("report on the idle server: %v", err)
	}
	if err := s.RoundEnd("eu1", gather.RoundReport{GatherID: 7, MapIndex: 1}); err != gather.ErrWrongID {
		t.Fatalf("report for another gather: %v", err)
	}
	if err := s.RoundEnd("zz", gather.RoundReport{GatherID: 1, MapIndex: 1}); err == nil {
		t.Fatal("report from nowhere was taken")
	}
}
