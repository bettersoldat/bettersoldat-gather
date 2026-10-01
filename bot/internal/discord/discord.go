// Package discord is the bot's face: it reads the !beta_ commands in the gather channel,
// answers them, and is the service's Notifier for announcements and DMs.
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
	s         *discordgo.Session
	svc       *service.Service
	channelID string
	prefix    string
}

// New makes the session; Run opens it.
func New(token, channelID, prefix string, svc *service.Service) (*Bot, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent | discordgo.IntentsGuilds
	b := &Bot{s: s, svc: svc, channelID: channelID, prefix: prefix}
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
	<-ctx.Done()
	return b.s.Close()
}

// Announce is a line in the gather channel.
func (b *Bot) Announce(text string) {
	if _, err := b.s.ChannelMessageSend(b.channelID, text); err != nil {
		log.Printf("discord: announcing: %v", err)
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
	if m.Author == nil || m.Author.Bot || m.ChannelID != b.channelID {
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
