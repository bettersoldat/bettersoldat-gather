// An end-to-end run of script/gather.lua on a real bettersoldat server, against a fake
// bot. It runs only when GATHER_E2E_SERVER names the server's binary and
// GATHER_E2E_DIR the directory to run it from (bettersoldat's root, for ./assets):
//
//	GATHER_E2E_SERVER=../bettersoldat/build/windows/x64/release/bettersoldat-server.exe \
//	GATHER_E2E_DIR=../bettersoldat go test ./internal/e2e -v
//
// The fake bot says a gather is live on three maps; the script must start the first,
// and as `nextmap` is typed at the server's console three times, report each round and
// move on: map 1, map 2, then (0-0 after two) the tiebreaker, and then say it is done.
package e2e

import (
	"bufio"
	"encoding/json"
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

var maps = []string{"ctf_Kampf", "ctf_Division", "ctf_Laos"}

type fakeBot struct {
	mu      sync.Mutex
	reports []gather.RoundReport
	events  []api.Event
}

func (f *fakeBot) handler() http.Handler {
	return api.Handler(secret, f)
}

func (f *fakeBot) State() api.State {
	return api.State{GatherID: 1, Phase: "live", Password: "playpw", SpecPassword: "specpw", Maps: maps,
		Teams: map[string][]string{"alpha": {"a"}, "bravo": {"b"}}}
}

func (f *fakeBot) RoundEnd(r gather.RoundReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, r)
	return nil
}

func (f *fakeBot) ServerEvent(e api.Event) {
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

	// the script, with the fake bot's address and a quick poll
	src, err := os.ReadFile(filepath.Join("..", "..", "script", "gather.lua"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, r := range []struct{ old, new string }{
		{`local bot_url = "http://127.0.0.1:8080"`, `local bot_url = "` + srv.URL + `"`},
		{`local secret = "change-me"`, `local secret = "` + secret + `"`},
		{`local poll_every = 3`, `local poll_every = 1`},
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
	waitFor := func(what string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for !strings.Contains(logged(), what) {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q in the server's output:\n%s", what, logged())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
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

	waitFor("gather: script loaded", 15*time.Second)
	waitFor("gather: #1 is live: ctf_Kampf, ctf_Division, ctf_Laos", 15*time.Second)
	waitFor("Gather #1, map 1 of 3: ctf_Kampf", 30*time.Second)

	for i, next := range []string{"ctf_Division", "ctf_Laos", ""} {
		io.WriteString(stdin, "nextmap\n")
		waitForReports(i+1, 30*time.Second)
		if next != "" {
			waitFor("Gather #1, map "+string(rune('2'+i))+" of 3: "+next, 30*time.Second)
		}
	}

	bot.mu.Lock()
	defer bot.mu.Unlock()
	if len(bot.reports) != 3 {
		t.Fatalf("%d reports", len(bot.reports))
	}
	for i, r := range bot.reports {
		if r.GatherID != 1 || r.MapIndex != i+1 || r.Map != maps[i] || r.Why != "nextmap" || r.Players == nil {
			raw, _ := json.Marshal(r)
			t.Errorf("report %d: %s", i+1, raw)
		}
		if r.Done != (i == 2) {
			t.Errorf("report %d: done=%v", i+1, r.Done)
		}
	}
	if !strings.Contains(logged(), "Gather #1 is over") {
		t.Errorf("the end was not told:\n%s", logged())
	}
}
