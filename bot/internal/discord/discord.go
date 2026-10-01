// Package discord is the bot's face: it reads the !beta_ commands in the gather
// channels, answers each in the channel it came from, and is the service's Notifier for
// announcements, which go to every gather channel, and DMs. The channels share the one
// gather: a player added in any of them is in the same queue.
package discord

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"

	"gatherbot/internal/gather"
	"gatherbot/internal/service"
)

// Bot is the Discord session around the service.
type Bot struct {
	s        *discordgo.Session
	svc      *service.Service
	channels []string // in the order given, for the announcements
	listens  map[string]bool
	prefix   string
}

// New makes the session; Run opens it.
func New(token string, channels []string, prefix string, svc *service.Service) (*Bot, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent | discordgo.IntentsGuilds
	b := &Bot{s: s, svc: svc, channels: channels, listens: map[string]bool{}, prefix: prefix}
	for _, c := range channels {
		b.listens[c] = true
	}
	s.AddHandler(b.onMessage)
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		log.Printf("discord: logged in as %s#%s", r.User.Username, r.User.Discriminator)
	})
	return b, nil
}

// Run opens the session until ctx ends.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.s.Open(); err != nil {
		return fmt.Errorf("opening the Discord session: %w", err)
	}
	// each channel, looked up once, so a channel the bot was never let into (not invited
	// to that server, or no View Channels there) says so in the log rather than in silence
	for _, c := range b.channels {
		if ch, err := b.s.Channel(c); err != nil {
			log.Printf("discord: channel %s is out of reach: %v", c, err)
		} else {
			log.Printf("discord: channel %s is #%s", c, ch.Name)
		}
	}
	<-ctx.Done()
	return b.s.Close()
}

// Announce is a line in every gather channel. One that fails (the bot not let in there)
// doesn't keep it from the others.
func (b *Bot) Announce(text string) {
	for _, c := range b.channels {
		if _, err := b.s.ChannelMessageSend(c, text); err != nil {
			log.Printf("discord: announcing in %s: %v", c, err)
		}
	}
}

// DM is a direct message to one user.
func (b *Bot) DM(userID, text string) error {
	ch, err := b.s.UserChannelCreate(userID)
	if err != nil {
		return err
	}
	_, err = b.s.ChannelMessageSend(ch.ID, text)
	return err
}

func (b *Bot) onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || !b.listens[m.ChannelID] {
		return
	}
	text := strings.TrimSpace(m.Content)
	if !strings.HasPrefix(strings.ToLower(text), strings.ToLower(b.prefix)) {
		return
	}
	rest := text[len(b.prefix):]
	cmd, args, _ := strings.Cut(rest, " ")
	cmd = strings.ToLower(cmd)
	args = strings.TrimSpace(args)
	p := gather.Player{ID: m.Author.ID, Name: displayName(m)}

	var reply string
	switch cmd {
	case "add", "join", "++":
		reply = b.svc.Add(p)
	case "del", "leave", "--", "remove":
		reply = b.svc.Del(p)
	case "status", "list", "who":
		reply = b.svc.Status()
	case "spec", "spectate":
		reply = b.svc.Spec(p, args)
	case "info":
		reply = b.svc.Info(p)
	case "maps", "pool":
		reply = b.svc.Maps()
	case "abort", "end", "reset":
		reply = b.svc.Abort(p, b.isAdmin(m), args)
	case "passwords", "password":
		reply = b.svc.Passwords(p)
		log.Printf("discord: %s (%s) asked for the passwords: %s", p.Name, p.ID, reply)
	case "help", "":
		reply = b.svc.Help()
	default:
		return
	}
	if reply == "" {
		return
	}
	if _, err := s.ChannelMessageSend(m.ChannelID, m.Author.Mention()+" "+reply); err != nil {
		log.Printf("discord: replying to %s: %v", cmd, err)
	}
}

// isAdmin: Manage Server or Administrator in the channel.
func (b *Bot) isAdmin(m *discordgo.MessageCreate) bool {
	perms, err := b.s.UserChannelPermissions(m.Author.ID, m.ChannelID)
	if err != nil {
		log.Printf("discord: permissions of %s: %v", m.Author.ID, err)
		return false
	}
	return perms&(discordgo.PermissionManageGuild|discordgo.PermissionAdministrator) != 0
}

// displayName: the server nickname, else the global name, else the username.
func displayName(m *discordgo.MessageCreate) string {
	if m.Member != nil && m.Member.Nick != "" {
		return m.Member.Nick
	}
	if m.Author.GlobalName != "" {
		return m.Author.GlobalName
	}
	return m.Author.Username
}
