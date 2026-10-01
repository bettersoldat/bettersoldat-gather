// An end-to-end run of server/gather.lua on a real SoldatReloaded server, against a fake
// bot. It runs only when GATHER_E2E_SERVER names the server's binary and
// GATHER_E2E_DIR the directory to run it from (SoldatReloaded's root, for ./assets),
// both absolute:
//
//	GATHER_E2E_SERVER=/abs/path/to/soldatreloaded/build/windows/x64/release/soldatreloaded-server.exe \
//	GATHER_E2E_DIR=/abs/path/to/soldatreloaded go test ./internal/e2e -v
//
// The fake bot says a gather is live with ctf_Laos as the tiebreaker. The chat
// commands are said as slot 0 through the console's `lua`, and `nextmap` typed at the
// console runs a map to its end: !map starts a counted map, !r replays it uncounted,
// a second map ends 0-0 so !tb plays the tiebreaker, and the third report says done.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gatherbot/internal/api"
	"gatherbot/internal/gather"
)

const secret = "e2e-secret"

var pool = []string{"ctf_Ash", "ctf_Kampf", "ctf_Division", "ctf_Laos"}

type fakeBot struct {
	mu      sync.Mutex
	reports []gather.RoundReport
	events  []api.Event
}

func (f *fakeBot) handler() http.Handler {
	return api.Handler(secret, f)
}

// the script says it is "e2e" with every request; anything else is refused
func (f *fakeBot) check(server string) error {
	if server != "e2e" {
		return fmt.Errorf("%w %q", api.ErrNoServer, server)
	}
	return nil
}

func (f *fakeBot) State(server string) (api.State, error) {
	if err := f.check(server); err != nil {
		return api.State{}, err
	}
	return api.State{Server: server, Version: 1, GatherID: 1, Phase: "live", Password: "playpw",
		Tiebreaker: "ctf_Laos", Pool: pool,
		Teams: map[string][]string{"alpha": {"a"}, "bravo": {"b"}}}, nil
}

// the state never changes here: a wait with the version runs to the request's end
func (f *fakeBot) WaitState(ctx context.Context, server string, since uint64) (api.State, error) {
	st, err := f.State(server)
	if err != nil {
		return st, err
	}
	if since >= st.Version {
		<-ctx.Done()
	}
	return st, nil
}

func (f *fakeBot) RoundEnd(server string, r gather.RoundReport) error {
	if err := f.check(server); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, r)
	return nil
}

func (f *fakeBot) ServerEvent(server string, e api.Event) {
	if f.check(server) != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeBot) reportCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

func TestScriptPlaysTheSeries(t *testing.T) {
	exe, dir := os.Getenv("GATHER_E2E_SERVER"), os.Getenv("GATHER_E2E_DIR")
	if exe == "" || dir == "" {
		t.Skip("GATHER_E2E_SERVER and GATHER_E2E_DIR are not set")
	}
	exe, _ = filepath.Abs(exe)
	dir, _ = filepath.Abs(dir)

	bot := &fakeBot{}
	srv := httptest.NewServer(bot.handler())
	defer srv.Close()

	// the script as shipped, but a say_to goes nowhere without a joined player: it is
	// printed too, so the test sees it
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "server", "gather.lua"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, r := range []struct{ old, new string }{
		{`local color = "7FD6FF"`, `local color = "7FD6FF"; local _say_to = server.say_to; server.say_to = function(slot, text, c) server.print("[to " .. slot .. "] " .. text); _say_to(slot, text, c) end`},
	} {
		if !strings.Contains(text, r.old) {
			t.Fatalf("script lacks %q", r.old)
		}
		text = strings.Replace(text, r.old, r.new, 1)
	}
	script := filepath.Join(t.TempDir(), "gather.lua")
	if err := os.WriteFile(script, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(exe, "+sv_port", "23999", "+map", "ctf_Ash", "+sv_maps", "", "+sv_script", script, "+sv_hostname", "e2e")
	cmd.Dir = dir
	// the settings, as the Docker image passes them
	cmd.Env = append(os.Environ(), "GATHER_BOT_URL="+srv.URL, "GATHER_SECRET="+secret, "GATHER_SERVER_NAME=e2e", "GATHER_RETRY=1")
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", exe, err)
	}
	defer func() {
		io.WriteString(stdin, "quit\n")
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
		}
	}()

	// the server's lines, as they come
	var logMu sync.Mutex
	var lines []string
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			logMu.Lock()
			lines = append(lines, sc.Text())
			logMu.Unlock()
		}
	}()
	logged := func() string {
		logMu.Lock()
		defer logMu.Unlock()
		return strings.Join(lines, "\n")
	}
	count := func(what string) int { return strings.Count(logged(), what) }
	waitForN := func(what string, n int, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for count(what) < n {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q %d times in the server's output:\n%s", what, n, logged())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitFor := func(what string, timeout time.Duration) { t.Helper(); waitForN(what, 1, timeout) }
	waitForReports := func(n int, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for bot.reportCount() < n {
			if time.Now().After(deadline) {
				t.Fatalf("only %d reports after %s:\n%s", bot.reportCount(), timeout, logged())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	// (the console takes its quotes off the line, so the strings go in Lua's brackets)
	say := func(text string) { io.WriteString(stdin, "lua on_chat(0, [["+text+"]], false)\n") }

	waitFor("gather: script loaded, listening to "+srv.URL+" as e2e", 15*time.Second)
	waitFor("gather: #1 is on; the tiebreaker is ctf_Laos", 15*time.Second)

	// the password is the server's own now: the script set sv_password to the bot's word
	waitFor("gather: the password is set", 15*time.Second)
	io.WriteString(stdin, "sv_password\n")
	waitFor("playpw", 10*time.Second)

	// nothing counts until a map is asked for
	say("!tb")
	waitFor("[to 0] No tiebreaker yet: 0 of 3 maps played", 10*time.Second)
	say("!r")
	waitFor("[to 0] No map of the gather is on", 10*time.Second)
	say("!map nope")
	waitFor("[to 0] No CTF map called 'nope'", 10*time.Second)

	// map 1: started, replayed with !r (uncounted), run to its end
	say("!map kampf")
	waitFor("someone starts map 1 of 3: ctf_Kampf", 10*time.Second)
	waitFor("Gather #1, map 1 of 3: ctf_Kampf. This one counts.", 30*time.Second)
	say("!r")
	waitFor("someone restarts the round on ctf_Kampf", 10*time.Second)
	waitForN("Gather #1, map 1 of 3: ctf_Kampf. This one counts.", 2, 30*time.Second)
	if bot.reportCount() != 0 {
		t.Fatalf("a restart was reported: %+v", bot.reports)
	}

	// pause, status, and the countdown back
	say("!p")
	waitFor("Game paused by someone", 10*time.Second)
	say("!status")
	waitFor("[to 0] Gather #1: live", 10*time.Second)
	waitFor("[to 0] Series: alpha 0 - 0 bravo, 0 of 3 maps played; this map counts. Tiebreaker: ctf_Laos.", 10*time.Second)
	waitFor("[to 0] Now on ctf_Kampf: alpha 0 - 0 bravo", 10*time.Second)
	if !strings.Contains(logged(), "left, paused") {
		t.Fatalf("!status did not say paused:\n%s", logged())
	}
	say("!up")
	waitFor("Go!", 10*time.Second)
	for _, n := range []string{"\n3\n", "\n2\n", "\n1\n"} {
		if !strings.Contains(logged(), n) {
			t.Fatalf("the countdown lacked %q:\n%s", n, logged())
		}
	}
	io.WriteString(stdin, "lua server.print([[paused: ]] .. tostring(server.paused()))\n")
	waitFor("paused: false", 10*time.Second)

	io.WriteString(stdin, "nextmap\n")
	waitForReports(1, 30*time.Second)
	waitFor("Map 1 of 3 over on ctf_Kampf", 10*time.Second)
	say("!tb")
	waitFor("[to 0] No tiebreaker yet: 1 of 3 maps played", 10*time.Second)

	// map 2, cut short by !map another and started again, then run to its end: 0-0
	say("!map ash")
	waitFor("someone starts map 2 of 3: ctf_Ash", 10*time.Second)
	waitFor("Gather #1, map 2 of 3: ctf_Ash. This one counts.", 30*time.Second)
	say("!map division")
	waitFor("The round on ctf_Ash is cut short", 10*time.Second)
	waitFor("Gather #1, map 2 of 3: ctf_Division. This one counts.", 30*time.Second)
	io.WriteString(stdin, "nextmap\n")
	waitForReports(2, 30*time.Second)
	waitFor("1-1: say !tb for the tiebreaker, ctf_Laos.", 10*time.Second)
	say("!map ash")
	waitFor("[to 0] It is 1-1: say !tb", 10*time.Second)

	// the tiebreaker
	say("!tb")
	waitFor("someone starts map 3 of 3: ctf_Laos (the tiebreaker)", 10*time.Second)
	waitFor("Gather #1, map 3 of 3: ctf_Laos. This one counts.", 30*time.Second)
	io.WriteString(stdin, "nextmap\n")
	waitForReports(3, 30*time.Second)
	waitFor("The series is over", 10*time.Second)

	bot.mu.Lock()
	reports := append([]gather.RoundReport(nil), bot.reports...)
	bot.mu.Unlock()
	if len(reports) != 3 {
		t.Fatalf("%d reports", len(reports))
	}
	for i, r := range reports {
		want := []string{"ctf_Kampf", "ctf_Division", "ctf_Laos"}[i]
		if r.GatherID != 1 || r.MapIndex != i+1 || r.Map != want || r.Why != "nextmap" || r.Players == nil {
			raw, _ := json.Marshal(r)
			t.Errorf("report %d: %s", i+1, raw)
		}
		if r.Done != (i == 2) {
			t.Errorf("report %d: done=%v", i+1, r.Done)
		}
	}

	// with the series over, !map changes the map freely
	say("!map ash")
	waitFor("someone changes the map to ctf_Ash", 10*time.Second)
	waitFor("Next map: ctf_Ash", 10*time.Second)
}
