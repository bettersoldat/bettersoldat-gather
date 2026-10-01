// gatherbot: a Discord gather bot for bettersoldat. It queues players in a channel,
// makes two teams when enough have joined, DMs them the server and its password, has
// each team pick a map (and picks the tiebreaker itself), and shows each round's end as
// the server's script reports it. See README.md.
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

	svc := service.New(service.Config{
		ServerAddr:  cfg.ServerAddr,
		TeamSize:    cfg.TeamSize,
		Pool:        cfg.Maps,
		PickTimeout: cfg.PickTimeout,
		Prefix:      cfg.Prefix,
		Grace:       cfg.Grace,
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

	log.Printf("gatherbot: %dv%d, prefix %q, server %s, %d maps in the pool", cfg.TeamSize, cfg.TeamSize, cfg.Prefix, cfg.ServerAddr, len(cfg.Maps))
	if err := bot.Run(ctx); err != nil {
		log.Printf("discord: %v", err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
}
