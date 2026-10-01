// gatherbot: a Discord gather bot for bettersoldat. It queues players in a channel,
// makes two teams when enough have joined, DMs them the server and its password, draws
// the tiebreaker, and shows each map's end as the server's script reports it; the maps
// themselves are picked in the game. See README.md.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"gatherbot/internal/api"
	"gatherbot/internal/config"
	"gatherbot/internal/discord"
	"gatherbot/internal/service"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	var servers []service.ServerConfig
	for _, s := range cfg.Servers {
		servers = append(servers, service.ServerConfig{Name: s.Name, Addr: s.Addr})
	}
	svc := service.New(service.Config{
		Servers:  servers,
		TeamSize: cfg.TeamSize,
		Pool:     cfg.Maps,
		Prefix:   cfg.Prefix,
		Grace:    cfg.Grace,
	}, nil)

	bot, err := discord.New(cfg.Token, cfg.ChannelID, cfg.Prefix, svc)
	if err != nil {
		log.Fatalf("discord: %v", err)
	}
	svc.SetNotifier(bot)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	server := &http.Server{Addr: cfg.Listen, Handler: api.Handler(cfg.Secret, svc), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("api: listening on %s", cfg.Listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("api: %v", err)
			stop()
		}
	}()

	for _, s := range cfg.Servers {
		log.Printf("gatherbot: server %s at %s", s.Name, s.Addr)
	}
	log.Printf("gatherbot: %dv%d, prefix %q, %d maps in the pool", cfg.TeamSize, cfg.TeamSize, cfg.Prefix, len(cfg.Maps))
	if err := bot.Run(ctx); err != nil {
		log.Printf("discord: %v", err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
}
