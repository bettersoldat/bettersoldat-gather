package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gatherbot/internal/gather"
)

type fakeBackend struct {
	state   State
	reports []gather.RoundReport
	events  []Event
	servers []string
	err     error
}

func (f *fakeBackend) State(server string) (State, error) {
	f.servers = append(f.servers, server)
	if server == "nowhere" {
		return State{}, fmt.Errorf("%w %q", ErrNoServer, server)
	}
	return f.state, nil
}

func (f *fakeBackend) WaitState(ctx context.Context, server string, since uint64) (State, error) {
	if since >= f.state.Version {
		<-ctx.Done() // nothing changes in the fake: the wait runs out
	}
	return f.State(server)
}

func (f *fakeBackend) RoundEnd(server string, r gather.RoundReport) error {
	f.servers = append(f.servers, server)
	f.reports = append(f.reports, r)
	return f.err
}

func (f *fakeBackend) ServerEvent(server string, e Event) {
	f.servers = append(f.servers, server)
	f.events = append(f.events, e)
}

func do(t *testing.T, h http.Handler, method, path, token, server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if server != "" {
		req.Header.Set(ServerHeader, server)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSecretRequired(t *testing.T) {
	h := Handler("s3cret", &fakeBackend{})
	if rec := do(t, h, "GET", "/api/state", "", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/state", "wrong", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/healthz", "", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
}

func TestState(t *testing.T) {
	b := &fakeBackend{state: State{Server: "eu1", Version: 1, GatherID: 4, Phase: "live", Password: "pw", Tiebreaker: "c", Pool: []string{"a", "b", "c"}, Teams: map[string][]string{"alpha": {"x"}, "bravo": {"y"}}}}
	h := Handler("s3cret", b)
	rec := do(t, h, "GET", "/api/state", "s3cret", "eu1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	var got State
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GatherID != 4 || got.Password != "pw" || got.Tiebreaker != "c" || len(got.Pool) != 3 || got.Teams["bravo"][0] != "y" || got.Server != "eu1" {
		t.Fatalf("state %+v", got)
	}
	if b.servers[0] != "eu1" {
		t.Fatalf("the server header did not reach the backend: %v", b.servers)
	}
	if rec := do(t, h, "GET", "/api/state", "s3cret", "nowhere", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown server: %d", rec.Code)
	}
	// since below the version answers at once; since at it waits for the request's end
	b.state.Version = 3
	if rec := do(t, h, "GET", "/api/state?since=2", "s3cret", "eu1", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":3`) {
		t.Fatalf("since 2: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/api/state?since=x", "s3cret", "eu1", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("since x: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/api/state?since=3", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set(ServerHeader, "eu1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	held := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(held, req.WithContext(ctx))
	if held.Code != http.StatusOK || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("since 3: %d after %s", held.Code, time.Since(start))
	}
}

func TestRoundAndEvent(t *testing.T) {
	b := &fakeBackend{}
	h := Handler("s3cret", b)
	body := `{"gather_id":2,"map_index":1,"map":"ctf_Ash","why":"limit","scores":{"alpha":5,"bravo":2},"winner":"alpha","players":[{"name":"p","team":"alpha","kills":3,"deaths":1,"flags":2,"ping":40}],"done":false}`
	if rec := do(t, h, "POST", "/api/round", "s3cret", "na1", body); rec.Code != http.StatusOK {
		t.Fatalf("round: %d %s", rec.Code, rec.Body)
	}
	if len(b.reports) != 1 || b.reports[0].Scores.Alpha != 5 || b.reports[0].Players[0].Flags != 2 || b.servers[0] != "na1" {
		t.Fatalf("reports %+v servers %v", b.reports, b.servers)
	}
	if rec := do(t, h, "POST", "/api/round", "s3cret", "", "{nope"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad round: %d", rec.Code)
	}
	b.err = gather.ErrNotLive
	if rec := do(t, h, "POST", "/api/round", "s3cret", "", body); rec.Code != http.StatusConflict {
		t.Fatalf("round while idle: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/event", "s3cret", "", `{"type":"join","slot":3,"name":"p"}`); rec.Code != http.StatusOK {
		t.Fatalf("event: %d %s", rec.Code, rec.Body)
	}
	if len(b.events) != 1 || b.events[0].Slot != 3 {
		t.Fatalf("events %+v", b.events)
	}
}
