// Package api is the HTTP side the game server's script talks to: it polls the gather's
// state (the password it must ask for, the maps to play), posts each round's end, and
// tells of joins and leaves. Every call carries the shared secret as a bearer token.
//
//	GET  /api/state          -> State
//	POST /api/round          <- gather.RoundReport
//	POST /api/event          <- Event
//	GET  /healthz            (no secret)
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"gatherbot/internal/gather"
)

// State is what the script needs to know, polled every few seconds.
type State struct {
	GatherID     int                 `json:"gather_id"`
	Phase        string              `json:"phase"` // "idle", "picking" or "live"
	Password     string              `json:"password"`
	SpecPassword string              `json:"spec_password"`
	Maps         []string            `json:"maps"`  // alpha's pick, bravo's, the tiebreaker; empty until live
	Teams        map[string][]string `json:"teams"` // "alpha" and "bravo", by Discord name
}

// Event is something that happened on the server.
type Event struct {
	Type string `json:"type"` // "join" or "leave"
	Slot int    `json:"slot"`
	Name string `json:"name"`
}

// Backend is what the API serves: the service.
type Backend interface {
	State() State
	RoundEnd(gather.RoundReport) error
	ServerEvent(Event)
}

// Handler is the API's HTTP handler.
func Handler(secret string, b Backend) http.Handler {
	if secret == "" {
		panic("api: the secret must be set")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/state", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.State())
	}))
	mux.HandleFunc("POST /api/round", auth(func(w http.ResponseWriter, r *http.Request) {
		var report gather.RoundReport
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&report); err != nil {
			http.Error(w, "bad report: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := b.RoundEnd(report); err != nil {
			status := http.StatusConflict
			if errors.Is(err, gather.ErrNotLive) || errors.Is(err, gather.ErrWrongID) {
				log.Printf("api: round report dropped: %v", err)
			} else {
				status = http.StatusInternalServerError
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/event", auth(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&ev); err != nil {
			http.Error(w, "bad event: "+err.Error(), http.StatusBadRequest)
			return
		}
		b.ServerEvent(ev)
		writeJSON(w, map[string]bool{"ok": true})
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: writing a response: %v", err)
	}
}
