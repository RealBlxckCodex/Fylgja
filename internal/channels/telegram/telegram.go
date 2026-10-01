// Package telegram ist der Telegram-Adapter (Spec 9.5) über die Bot-API (HTTP).
// Default Long-Polling (kein Inbound-Port), optional Webhook mit Secret-Token.
package telegram

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/realblxckcodex/fylgja/internal/channels"
)

// Bot ist ein Telegram-Bot (ein Bot pro Fylgja möglich).
type Bot struct {
	Token         string
	APIBase       string // https://api.telegram.org
	Mode          string // polling|webhook
	WebhookSecret string
	MiniAppURL    string // https://…/tg: setzt den Menü-Knopf des Bots auf die Mini App
	HTTP          *http.Client
	Log           *slog.Logger

	id       string
	username string
	handler  channels.InboundHandler
	mu       sync.Mutex
	lastSend map[string]time.Time
	health   channels.Health
}

func (b *Bot) Platform() string { return "telegram" }
func (b *Bot) ID() string       { return b.id }

func (b *Bot) Capabilities() channels.Capabilities {
	return channels.Capabilities{Threads: true, Buttons: true, Edits: true, Voice: true, MaxLen: 4096, MaxFileBytes: 50 << 20,
		Dialect: "markdownv2", EditInterval: time.Second}
}

func (b *Bot) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

func (b *Bot) base() string {
	if b.APIBase == "" {
		return "https://api.telegram.org"
	}
	return strings.TrimRight(b.APIBase, "/")
}

// APIError ist ein Fehler der Bot-API.
type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram: %d %s", e.Code, e.Description) }

func (b *Bot) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base()+"/bot"+b.Token+"/"+method, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		hc := b.HTTP
		if hc == nil {
			hc = &http.Client{Timeout: 70 * time.Second}
		}
		resp, err := hc.Do(req)
		if err != nil {
			// Token nie in Fehlermeldungen/Logs.
			return errors.New(strings.ReplaceAll(err.Error(), b.Token, "***"))
		}
		var r struct {
			OK          bool            `json:"ok"`
			Result      json.RawMessage `json:"result"`
			ErrorCode   int             `json:"error_code"`
			Description string          `json:"description"`
			Parameters  struct {
				RetryAfter int `json:"retry_after"`
			} `json:"parameters"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("telegram: %s: %w", method, err)
		}
		if r.OK {
			if out != nil {
				return json.Unmarshal(r.Result, out)
			}
			return nil
		}
		ae := &APIError{Code: r.ErrorCode, Description: r.Description, RetryAfter: r.Parameters.RetryAfter}
		if r.ErrorCode == 429 && r.Parameters.RetryAfter > 0 && r.Parameters.RetryAfter < 60 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(r.Parameters.RetryAfter) * time.Second):
			}
			continue
		}
		return ae
	}
	return errors.New("telegram: zu viele wiederholungen")
}

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	IsForum bool   `json:"is_forum"`
}

type tgEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type tgFile struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
}

type tgMessage struct {
	MessageID       int64      `json:"message_id"`
	MessageThreadID int64      `json:"message_thread_id"`
	From            *tgUser    `json:"from"`
	Chat            tgChat     `json:"chat"`
	Date            int64      `json:"date"`
	Text            string     `json:"text"`
	Caption         string     `json:"caption"`
	Entities        []tgEntity `json:"entities"`
	CaptionEntities []tgEntity `json:"caption_entities"`
	ReplyTo         *tgMessage `json:"reply_to_message"`
	Voice           *tgFile    `json:"voice"`
	Audio           *tgFile    `json:"audio"`
	Document        *tgFile    `json:"document"`
	Photo           []tgFile   `json:"photo"`
}

type tgUpdate struct {
	UpdateID      int64      `json:"update_id"`
	Message       *tgMessage `json:"message"`
	CallbackQuery *struct {
		ID      string     `json:"id"`
		From    tgUser     `json:"from"`
		Message *tgMessage `json:"message"`
		Data    string     `json:"data"`
	} `json:"callback_query"`
	MessageReaction *struct {
		Chat        tgChat  `json:"chat"`
		MessageID   int64   `json:"message_id"`
		User        *tgUser `json:"user"`
		NewReaction []struct {
			Type  string `json:"type"`
			Emoji string `json:"emoji"`
		} `json:"new_reaction"`
	} `json:"message_reaction"`
}

// utf16Slice schneidet Text nach UTF-16-Offsets (Telegram-Entities).
func utf16Slice(s string, off, length int) string {
	var units []uint16
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	if off < 0 || off+length > len(units) {
		return ""
	}
	seg := units[off : off+length]
	var out []rune
	for i := 0; i < len(seg); i++ {
		u := seg[i]
		if u >= 0xD800 && u < 0xDC00 && i+1 < len(seg) {
			out = append(out, rune(u-0xD800)<<10+rune(seg[i+1]-0xDC00)+0x10000)
			i++
		} else {
			out = append(out, rune(u))
		}
	}
	return string(out)
}

func display(u *tgUser) string {
	if u == nil {
		return ""
	}
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		n += " (@" + u.Username + ")"
	}
	return n
}

// Convert normalisiert ein Update (exportiert für Tests).
func (b *Bot) Convert(u tgUpdate) (channels.InboundEvent, bool) {
	ev := channels.InboundEvent{Platform: "telegram", BotID: b.id, ReceivedAt: time.Now(), Raw: u}
	switch {
	case u.Message != nil:
		m := u.Message
		if m.From == nil {
			return ev, false
		}
		ev.ChatID = strconv.FormatInt(m.Chat.ID, 10)
		if m.MessageThreadID != 0 && m.Chat.IsForum {
			ev.ThreadID = strconv.FormatInt(m.MessageThreadID, 10)
		}
		ev.MessageID = strconv.FormatInt(m.MessageID, 10)
		ev.Sender = channels.Sender{PlatformUserID: strconv.FormatInt(m.From.ID, 10), Display: display(m.From), IsBot: m.From.IsBot}
		ev.IsDM = m.Chat.Type == "private"
		ev.Text = m.Text
		ents := m.Entities
		if ev.Text == "" {
			ev.Text, ents = m.Caption, m.CaptionEntities
		}
		for _, e := range ents {
			seg := utf16Slice(ev.Text, e.Offset, e.Length)
			switch e.Type {
			case "mention":
				ev.Mentions = append(ev.Mentions, strings.TrimPrefix(seg, "@"))
				if b.username != "" && strings.EqualFold(strings.TrimPrefix(seg, "@"), b.username) {
					ev.MentionsBot = true
				}
			case "bot_command":
				if e.Offset == 0 {
					cmd := strings.TrimPrefix(seg, "/")
					if name, target, ok := strings.Cut(cmd, "@"); ok {
						if b.username != "" && !strings.EqualFold(target, b.username) {
							continue // Befehl für einen anderen Bot
						}
						cmd = name
						ev.MentionsBot = true
					}
					ev.Command = strings.ToLower(cmd)
					ev.Args = strings.TrimSpace(ev.Text[len(seg):])
				}
			}
		}
		if m.ReplyTo != nil {
			ev.ReplyTo = strconv.FormatInt(m.ReplyTo.MessageID, 10)
			ev.ReplyToBot = m.ReplyTo.From != nil && strconv.FormatInt(m.ReplyTo.From.ID, 10) == b.id
		}
		if m.Voice != nil {
			ev.Attachments = append(ev.Attachments, channels.Attachment{Kind: "voice", FileID: m.Voice.FileID, Mime: firstNonEmpty(m.Voice.MimeType, "audio/ogg"), Size: m.Voice.FileSize, Name: "voice.ogg"})
		}
		if m.Audio != nil {
			ev.Attachments = append(ev.Attachments, channels.Attachment{Kind: "audio", FileID: m.Audio.FileID, Mime: m.Audio.MimeType, Size: m.Audio.FileSize, Name: m.Audio.FileName})
		}
		if m.Document != nil {
			ev.Attachments = append(ev.Attachments, channels.Attachment{Kind: "file", FileID: m.Document.FileID, Mime: m.Document.MimeType, Size: m.Document.FileSize, Name: m.Document.FileName})
		}
		if len(m.Photo) > 0 {
			p := m.Photo[len(m.Photo)-1] // größte Auflösung
			ev.Attachments = append(ev.Attachments, channels.Attachment{Kind: "image", FileID: p.FileID, Mime: "image/jpeg", Size: p.FileSize, Name: "photo.jpg"})
		}
		return ev, true
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		ev.Sender = channels.Sender{PlatformUserID: strconv.FormatInt(cq.From.ID, 10), Display: display(&cq.From)}
		ev.Callback, ev.CallbackID = cq.Data, cq.ID
		if cq.Message != nil {
			ev.ChatID = strconv.FormatInt(cq.Message.Chat.ID, 10)
			ev.MessageID = strconv.FormatInt(cq.Message.MessageID, 10)
			ev.IsDM = cq.Message.Chat.Type == "private"
		}
		ev.MessageID = "cb:" + cq.ID
		return ev, true
	case u.MessageReaction != nil && u.MessageReaction.User != nil:
		r := u.MessageReaction
		ev.ChatID = strconv.FormatInt(r.Chat.ID, 10)
		ev.ReplyTo = strconv.FormatInt(r.MessageID, 10)
		ev.MessageID = fmt.Sprintf("react:%d:%d", r.MessageID, u.UpdateID)
		ev.Sender = channels.Sender{PlatformUserID: strconv.FormatInt(r.User.ID, 10), Display: display(r.User)}
		for _, x := range r.NewReaction {
			if x.Type == "emoji" {
				ev.Reaction = x.Emoji
			}
		}
		return ev, ev.Reaction != ""
	}
	return ev, false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var commands = []map[string]string{
	{"command": "status", "description": "Was läuft gerade?"},
	{"command": "tasks", "description": "Offene Aufgaben"},
	{"command": "approve", "description": "Offene Freigaben"},
	{"command": "stop", "description": "Aktuellen Lauf stoppen"},
	{"command": "queue", "description": "Nachricht nach dem aktuellen Lauf einreihen"},
	{"command": "remember", "description": "Etwas merken"},
	{"command": "forget", "description": "Etwas vergessen"},
	{"command": "rules", "description": "Regeln anzeigen"},
	{"command": "autonomy", "description": "Autonomiestufe anzeigen"},
	{"command": "pause", "description": "Fylgja pausieren"},
	{"command": "resume", "description": "Fylgja fortsetzen"},
	{"command": "digest", "description": "Zusammenfassung jetzt senden"},
	{"command": "computer", "description": "Link zum Computer"},
	{"command": "pair", "description": "Konto verknüpfen: /pair CODE"},
}

// Start initialisiert den Bot und startet Long-Polling (bzw. wartet im Webhook-Modus).
func (b *Bot) Start(ctx context.Context, h channels.InboundHandler) error {
	b.handler = h
	var me tgUser
	if err := b.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return err
	}
	b.id, b.username = strconv.FormatInt(me.ID, 10), me.Username
	_ = b.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
	if b.MiniAppURL != "" {
		if err := b.SetMenuButton(ctx, b.MiniAppURL); err != nil {
			b.log().Warn("mini-app-knopf", "err", err)
		}
	}
	b.setHealth(true, "verbunden als @"+me.Username)
	if b.Mode == "webhook" {
		<-ctx.Done()
		return nil
	}
	_ = b.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
	var offset int64
	for ctx.Err() == nil {
		var ups []tgUpdate
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 30,
			"allowed_updates": []string{"message", "callback_query", "message_reaction"}}, &ups)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.setHealth(false, err.Error())
			b.log().Warn("telegram polling", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			continue
		}
		b.setHealth(true, "polling")
		for _, u := range ups {
			offset = u.UpdateID + 1
			if ev, ok := b.Convert(u); ok {
				h(ctx, ev)
			}
		}
	}
	return nil
}

func (b *Bot) setHealth(ok bool, detail string) {
	b.mu.Lock()
	if b.health.OK != ok {
		b.health.Since = time.Now()
	}
	b.health.OK, b.health.Detail = ok, detail
	b.mu.Unlock()
}

func (b *Bot) Health() channels.Health {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.health
}

// ServeHTTP ist der Webhook-Endpunkt (Secret-Token-Prüfung in konstanter Zeit).
func (b *Bot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if b.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(b.WebhookSecret)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var u tgUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if ev, ok := b.Convert(u); ok && b.handler != nil {
		go b.handler(context.WithoutCancel(r.Context()), ev)
	}
	w.WriteHeader(http.StatusOK)
}

// throttle hält ~1 Nachricht/Sekunde pro Chat ein.
func (b *Bot) throttle(ctx context.Context, chat string) {
	b.mu.Lock()
	if b.lastSend == nil {
		b.lastSend = map[string]time.Time{}
	}
	wait := time.Until(b.lastSend[chat].Add(time.Second))
	b.lastSend[chat] = time.Now().Add(max(wait, 0))
	b.mu.Unlock()
	if wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

func target(to channels.Target) map[string]any {
	p := map[string]any{"chat_id": to.ChatID}
	if to.ThreadID != "" {
		p["message_thread_id"], _ = strconv.ParseInt(to.ThreadID, 10, 64)
	}
	if to.ReplyTo != "" {
		id, _ := strconv.ParseInt(to.ReplyTo, 10, 64)
		p["reply_parameters"] = map[string]any{"message_id": id, "allow_sending_without_reply": true}
	}
	return p
}

func (b *Bot) sendText(ctx context.Context, to channels.Target, text string, markup any) (string, error) {
	var last string
	parts := channels.Split(text, 4000)
	for i, part := range parts {
		b.throttle(ctx, to.ChatID)
		p := target(to)
		p["text"], p["parse_mode"] = part, "MarkdownV2"
		p["link_preview_options"] = map[string]bool{"is_disabled": true}
		if markup != nil && i == len(parts)-1 {
			p["reply_markup"] = markup
		}
		var m tgMessage
		err := b.call(ctx, "sendMessage", p, &m)
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == 400 && strings.Contains(ae.Description, "parse") {
			delete(p, "parse_mode")
			p["text"] = unescapeV2(part)
			err = b.call(ctx, "sendMessage", p, &m)
		}
		if err != nil {
			return last, err
		}
		last = strconv.FormatInt(m.MessageID, 10)
	}
	return last, nil
}

func unescapeV2(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		if r == '\\' && !esc {
			esc = true
			continue
		}
		esc = false
		b.WriteRune(r)
	}
	return b.String()
}

func (b *Bot) Send(ctx context.Context, to channels.Target, m channels.RichMessage) (channels.MessageRef, error) {
	id, err := b.sendText(ctx, to, channels.RenderTelegram(m), nil)
	return channels.MessageRef{Target: to, MessageID: id}, err
}

func (b *Bot) Edit(ctx context.Context, ref channels.MessageRef, m channels.RichMessage) error {
	text := channels.RenderTelegram(m)
	if parts := channels.Split(text, 4000); len(parts) > 1 {
		text = parts[0]
	}
	id, _ := strconv.ParseInt(ref.MessageID, 10, 64)
	err := b.call(ctx, "editMessageText", map[string]any{"chat_id": ref.Target.ChatID, "message_id": id, "text": text, "parse_mode": "MarkdownV2",
		"link_preview_options": map[string]bool{"is_disabled": true}}, nil)
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "not modified") {
		return nil
	}
	return err
}

func (b *Bot) React(ctx context.Context, ref channels.MessageRef, emoji string) error {
	id, _ := strconv.ParseInt(ref.MessageID, 10, 64)
	return b.call(ctx, "setMessageReaction", map[string]any{"chat_id": ref.Target.ChatID, "message_id": id,
		"reaction": []map[string]string{{"type": "emoji", "emoji": emoji}}}, nil)
}

func (b *Bot) Typing(ctx context.Context, to channels.Target) error {
	p := map[string]any{"chat_id": to.ChatID, "action": "typing"}
	if to.ThreadID != "" {
		p["message_thread_id"], _ = strconv.ParseInt(to.ThreadID, 10, 64)
	}
	return b.call(ctx, "sendChatAction", p, nil)
}

func keyboard(a channels.ApprovalCard) any {
	if a.Resolved != "" {
		return map[string]any{"inline_keyboard": [][]any{}}
	}
	var row []map[string]string
	if a.StepUp {
		row = append(row, map[string]string{"text": "🔐 In Web-UI bestätigen", "url": a.DeepLink})
	} else {
		row = append(row, map[string]string{"text": "✅ Freigeben", "callback_data": a.ApproveData})
	}
	row = append(row, map[string]string{"text": "❌ Ablehnen", "callback_data": a.DenyData})
	rows := [][]map[string]string{row}
	extra := []map[string]string{}
	if a.AlwaysData != "" && !a.StepUp {
		extra = append(extra, map[string]string{"text": "♾️ Immer für diesen Fall", "callback_data": a.AlwaysData})
	}
	if a.DeepLink != "" {
		extra = append(extra, map[string]string{"text": "✏️ Bearbeiten", "url": a.DeepLink})
	}
	if len(extra) > 0 {
		rows = append(rows, extra)
	}
	return map[string]any{"inline_keyboard": rows}
}

func (b *Bot) SendApproval(ctx context.Context, to channels.Target, a channels.ApprovalCard) (channels.MessageRef, error) {
	id, err := b.sendText(ctx, to, channels.RenderTelegram(channels.ApprovalText(a)), keyboard(a))
	return channels.MessageRef{Target: to, MessageID: id}, err
}

func (b *Bot) UpdateApproval(ctx context.Context, ref channels.MessageRef, a channels.ApprovalCard) error {
	id, _ := strconv.ParseInt(ref.MessageID, 10, 64)
	text := channels.RenderTelegram(channels.ApprovalText(a))
	err := b.call(ctx, "editMessageText", map[string]any{"chat_id": ref.Target.ChatID, "message_id": id, "text": text, "parse_mode": "MarkdownV2",
		"reply_markup": keyboard(a)}, nil)
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "not modified") {
		return nil
	}
	return err
}

func (b *Bot) AnswerCallback(ctx context.Context, id, text string) error {
	return b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// Download lädt eine Datei (Bot-API-Limit 20 MB).
func (b *Bot) Download(ctx context.Context, a channels.Attachment) ([]byte, error) {
	if a.Size > 20<<20 {
		return nil, errors.New("telegram: datei größer als 20 MB (lokaler bot-api-server nötig)")
	}
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := b.call(ctx, "getFile", map[string]any{"file_id": a.FileID}, &f); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base()+"/file/bot"+b.Token+"/"+f.FilePath, nil)
	if err != nil {
		return nil, err
	}
	hc := b.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, errors.New(strings.ReplaceAll(err.Error(), b.Token, "***"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("telegram: download http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 21<<20))
}

// SendVoice sendet eine Sprachnachricht (TTS-Antwort, OGG/Opus).
func (b *Bot) SendVoice(ctx context.Context, to channels.Target, ogg []byte) error {
	var buf bytes.Buffer
	boundary := "fylgjaVoiceBoundary"
	fmt.Fprintf(&buf, "--%s\r\nContent-Disposition: form-data; name=\"chat_id\"\r\n\r\n%s\r\n", boundary, to.ChatID)
	fmt.Fprintf(&buf, "--%s\r\nContent-Disposition: form-data; name=\"voice\"; filename=\"voice.ogg\"\r\nContent-Type: audio/ogg\r\n\r\n", boundary)
	buf.Write(ogg)
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base()+"/bot"+b.Token+"/sendVoice", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	hc := b.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), b.Token, "***"))
	}
	resp.Body.Close()
	return nil
}
