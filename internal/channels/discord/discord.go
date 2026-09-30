// Package discord ist der Discord-Adapter (Spec 9.4) auf Basis von discordgo.
// Ein Bot pro Fylgja (eigene Identität) oder Shared-Bot-Modus.
package discord

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/realblxckcodex/fylgja/internal/channels"
)

// Bot ist ein Discord-Bot.
type Bot struct {
	Token          string
	MessageContent bool // Intent nur, wenn Server-Channels gelesen werden sollen
	Log            *slog.Logger

	s        *discordgo.Session
	id       string
	mu       sync.Mutex
	health   channels.Health
	handler  channels.InboundHandler
	lastEdit map[string]time.Time
}

func (b *Bot) Platform() string { return "discord" }
func (b *Bot) ID() string       { return b.id }

func (b *Bot) Capabilities() channels.Capabilities {
	return channels.Capabilities{Threads: true, Buttons: true, Edits: true, Voice: true, MaxLen: 2000, MaxFileBytes: 25 << 20,
		Dialect: "discord", EditInterval: 2 * time.Second}
}

func (b *Bot) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

// Slash-Commands (9.4).
var slash = []*discordgo.ApplicationCommand{
	{Name: "status", Description: "Was läuft gerade?"},
	{Name: "tasks", Description: "Offene Aufgaben"},
	{Name: "approve", Description: "Offene Freigaben anzeigen"},
	{Name: "pause", Description: "Fylgja pausieren"},
	{Name: "resume", Description: "Fylgja fortsetzen"},
	{Name: "stop", Description: "Aktuellen Lauf stoppen"},
	{Name: "queue", Description: "Nachricht einreihen", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Nachricht", Required: true}}},
	{Name: "computer", Description: "Link zum Computer der Fylgja"},
	{Name: "remember", Description: "Etwas merken", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Was?", Required: true}}},
	{Name: "forget", Description: "Etwas vergessen", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionString, Name: "thema", Description: "Thema", Required: true}}},
	{Name: "rules", Description: "Regeln anzeigen"},
	{Name: "autonomy", Description: "Autonomiestufe anzeigen"},
	{Name: "digest", Description: "Zusammenfassung jetzt"},
	{Name: "pair", Description: "Konto verknüpfen", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionString, Name: "code", Description: "Pairing-Code", Required: true}}},
	{Name: "team", Description: "Team-Status"},
	{Name: "graph", Description: "Task-Graph anzeigen", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Graph-ID", Required: false}}},
}

func (b *Bot) Start(ctx context.Context, h channels.InboundHandler) error {
	s, err := discordgo.New("Bot " + b.Token)
	if err != nil {
		return err
	}
	b.s, b.handler = s, h
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsGuildMessageReactions | discordgo.IntentsDirectMessageReactions
	if b.MessageContent {
		s.Identify.Intents |= discordgo.IntentMessageContent
	}
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		b.id = r.User.ID
		b.setHealth(true, "verbunden als "+r.User.Username)
		for _, c := range slash {
			if _, err := s.ApplicationCommandCreate(r.User.ID, "", c); err != nil {
				b.log().Warn("discord slash-command", "cmd", c.Name, "err", err)
			}
		}
	})
	s.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if ev, ok := b.convertMessage(m.Message); ok {
			h(ctx, ev)
		}
	})
	s.AddHandler(func(_ *discordgo.Session, i *discordgo.InteractionCreate) {
		if ev, ok := b.convertInteraction(i); ok {
			h(ctx, ev)
		}
	})
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.MessageReactionAdd) {
		if r.UserID == b.id {
			return
		}
		h(ctx, channels.InboundEvent{Platform: "discord", BotID: b.id, ChatID: r.ChannelID, MessageID: "react:" + r.MessageID + ":" + r.UserID + ":" + r.Emoji.Name,
			ReplyTo: r.MessageID, Reaction: r.Emoji.Name, IsDM: r.GuildID == "", Sender: channels.Sender{PlatformUserID: r.UserID}, ReceivedAt: time.Now()})
	})
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { b.setHealth(false, "getrennt") })
	if err := s.Open(); err != nil {
		b.setHealth(false, err.Error())
		return err
	}
	<-ctx.Done()
	return s.Close()
}

func (b *Bot) setHealth(ok bool, d string) {
	b.mu.Lock()
	if b.health.OK != ok {
		b.health.Since = time.Now()
	}
	b.health.OK, b.health.Detail = ok, d
	b.mu.Unlock()
}

func (b *Bot) Health() channels.Health {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.health
}

// ConvertMessage ist exportiert für Tests.
func (b *Bot) convertMessage(m *discordgo.Message) (channels.InboundEvent, bool) {
	if m.Author == nil || m.Author.ID == b.id {
		return channels.InboundEvent{}, false
	}
	ev := channels.InboundEvent{Platform: "discord", BotID: b.id, ChatID: m.ChannelID, MessageID: m.ID, IsDM: m.GuildID == "",
		Sender: channels.Sender{PlatformUserID: m.Author.ID, Display: m.Author.Username, IsBot: m.Author.Bot}, Text: m.Content, ReceivedAt: time.Now(), Raw: m}
	for _, u := range m.Mentions {
		ev.Mentions = append(ev.Mentions, u.ID)
		if u.ID == b.id {
			ev.MentionsBot = true
		}
	}
	if b.id != "" {
		ev.Text = strings.TrimSpace(strings.NewReplacer("<@"+b.id+">", "", "<@!"+b.id+">", "").Replace(ev.Text))
	}
	if m.MessageReference != nil {
		ev.ReplyTo = m.MessageReference.MessageID
		if m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil && m.ReferencedMessage.Author.ID == b.id {
			ev.ReplyToBot = true
		}
	}
	for _, a := range m.Attachments {
		kind := "file"
		switch {
		case strings.HasPrefix(a.ContentType, "image/"):
			kind = "image"
		case strings.HasPrefix(a.ContentType, "audio/"):
			kind = "audio"
			if a.Filename == "voice-message.ogg" {
				kind = "voice"
			}
		}
		ev.Attachments = append(ev.Attachments, channels.Attachment{Kind: kind, Name: a.Filename, Mime: a.ContentType, Size: int64(a.Size), URL: a.URL})
	}
	return ev, true
}

func (b *Bot) convertInteraction(i *discordgo.InteractionCreate) (channels.InboundEvent, bool) {
	user := i.User
	if i.Member != nil {
		user = i.Member.User
	}
	if user == nil {
		return channels.InboundEvent{}, false
	}
	ev := channels.InboundEvent{Platform: "discord", BotID: b.id, ChatID: i.ChannelID, MessageID: "ia:" + i.ID, IsDM: i.GuildID == "",
		Sender: channels.Sender{PlatformUserID: user.ID, Display: user.Username}, CallbackID: i.ID + ":" + i.Token, ReceivedAt: time.Now(), MentionsBot: true, Raw: i}
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		d := i.ApplicationCommandData()
		ev.Command = d.Name
		var args []string
		for _, o := range d.Options {
			args = append(args, fmt.Sprint(o.Value))
		}
		ev.Args = strings.Join(args, " ")
		// Sofort bestätigen (3-s-Frist); die Antwort folgt als normale Nachricht.
		if b.s != nil {
			_ = b.s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{Content: "⏳ /" + d.Name, Flags: discordgo.MessageFlagsEphemeral}})
		}
	case discordgo.InteractionMessageComponent:
		ev.Callback = i.MessageComponentData().CustomID
		if i.Message != nil {
			ev.ReplyTo = i.Message.ID
		}
	default:
		return ev, false
	}
	return ev, true
}

func (b *Bot) session() (*discordgo.Session, error) {
	if b.s == nil {
		return nil, errors.New("discord: nicht verbunden")
	}
	return b.s, nil
}

func (b *Bot) Send(ctx context.Context, to channels.Target, m channels.RichMessage) (channels.MessageRef, error) {
	s, err := b.session()
	if err != nil {
		return channels.MessageRef{}, err
	}
	ch := to.ChatID
	if to.ThreadID != "" {
		ch = to.ThreadID
	}
	var last string
	for i, part := range channels.Split(channels.RenderDiscord(m), 2000) {
		ms := &discordgo.MessageSend{Content: part, AllowedMentions: &discordgo.MessageAllowedMentions{}}
		if i == 0 && to.ReplyTo != "" {
			ms.Reference = &discordgo.MessageReference{MessageID: to.ReplyTo, ChannelID: ch}
		}
		msg, err := s.ChannelMessageSendComplex(ch, ms, discordgo.WithContext(ctx))
		if err != nil {
			return channels.MessageRef{Target: to, MessageID: last}, err
		}
		last = msg.ID
	}
	return channels.MessageRef{Target: to, MessageID: last}, nil
}

func (b *Bot) Edit(ctx context.Context, ref channels.MessageRef, m channels.RichMessage) error {
	s, err := b.session()
	if err != nil {
		return err
	}
	ch := ref.Target.ChatID
	if ref.Target.ThreadID != "" {
		ch = ref.Target.ThreadID
	}
	text := channels.Split(channels.RenderDiscord(m), 2000)[0]
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: ref.MessageID, Channel: ch, Content: &text, AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx))
	return err
}

func (b *Bot) React(ctx context.Context, ref channels.MessageRef, emoji string) error {
	s, err := b.session()
	if err != nil {
		return err
	}
	return s.MessageReactionAdd(ref.Target.ChatID, ref.MessageID, emoji, discordgo.WithContext(ctx))
}

func (b *Bot) Typing(ctx context.Context, to channels.Target) error {
	s, err := b.session()
	if err != nil {
		return err
	}
	return s.ChannelTyping(to.ChatID, discordgo.WithContext(ctx))
}

func components(a channels.ApprovalCard) []discordgo.MessageComponent {
	if a.Resolved != "" {
		return []discordgo.MessageComponent{}
	}
	var btns []discordgo.MessageComponent
	if a.StepUp {
		btns = append(btns, discordgo.Button{Label: "In Web-UI bestätigen", Style: discordgo.LinkButton, URL: a.DeepLink})
	} else {
		btns = append(btns, discordgo.Button{Label: "Freigeben", Style: discordgo.SuccessButton, CustomID: a.ApproveData})
	}
	btns = append(btns, discordgo.Button{Label: "Ablehnen", Style: discordgo.DangerButton, CustomID: a.DenyData})
	if a.AlwaysData != "" && !a.StepUp {
		btns = append(btns, discordgo.Button{Label: "Immer für diesen Fall", Style: discordgo.SecondaryButton, CustomID: a.AlwaysData})
	}
	if a.DeepLink != "" && !a.StepUp {
		btns = append(btns, discordgo.Button{Label: "Bearbeiten", Style: discordgo.LinkButton, URL: a.DeepLink})
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: btns}}
}

func (b *Bot) SendApproval(ctx context.Context, to channels.Target, a channels.ApprovalCard) (channels.MessageRef, error) {
	s, err := b.session()
	if err != nil {
		return channels.MessageRef{}, err
	}
	text := channels.Split(channels.RenderDiscord(channels.ApprovalText(a)), 2000)[0]
	msg, err := s.ChannelMessageSendComplex(to.ChatID, &discordgo.MessageSend{Content: text, Components: components(a), AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx))
	if err != nil {
		return channels.MessageRef{}, err
	}
	return channels.MessageRef{Target: to, MessageID: msg.ID}, nil
}

func (b *Bot) UpdateApproval(ctx context.Context, ref channels.MessageRef, a channels.ApprovalCard) error {
	s, err := b.session()
	if err != nil {
		return err
	}
	text := channels.Split(channels.RenderDiscord(channels.ApprovalText(a)), 2000)[0]
	comps := components(a)
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: ref.MessageID, Channel: ref.Target.ChatID, Content: &text, Components: &comps}, discordgo.WithContext(ctx))
	return err
}

// AnswerCallback bestätigt einen Button-Klick (Deferred Update).
func (b *Bot) AnswerCallback(ctx context.Context, id, text string) error {
	s, err := b.session()
	if err != nil {
		return err
	}
	iid, tok, ok := strings.Cut(id, ":")
	if !ok {
		return errors.New("discord: ungültige callback-id")
	}
	return s.InteractionRespond(&discordgo.Interaction{ID: iid, Token: tok, AppID: b.id}, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: text, Flags: discordgo.MessageFlagsEphemeral}}, discordgo.WithContext(ctx))
}

func (b *Bot) Download(ctx context.Context, a channels.Attachment) ([]byte, error) {
	if !strings.HasPrefix(a.URL, "https://cdn.discordapp.com/") && !strings.HasPrefix(a.URL, "https://media.discordapp.net/") {
		return nil, errors.New("discord: unerwartete anhang-url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 26<<20))
}
