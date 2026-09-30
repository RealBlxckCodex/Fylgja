// Package channels abstrahiert Chat-Plattformen (Spec Kapitel 9). Logik kennt keine
// Plattform: Adapter normalisieren Inbound-Ereignisse und rendern RichMessages.
package channels

import (
	"context"
	"time"
)

// Capabilities einer Plattform.
type Capabilities struct {
	Threads         bool          `json:"threads"`
	Buttons         bool          `json:"buttons"`
	Edits           bool          `json:"edits"`
	Voice           bool          `json:"voice"`
	MaxLen          int           `json:"max_len"`
	MaxFileBytes    int64         `json:"max_file_bytes"`
	Dialect         string        `json:"markdown_dialect"` // markdownv2|discord|html|plain
	EditInterval    time.Duration `json:"edit_interval"`
}

// Target adressiert einen Chat/Thread.
type Target struct {
	Platform string `json:"platform"`
	ChatID   string `json:"chat_id"`
	ThreadID string `json:"thread_id,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty"`
}

// MessageRef referenziert eine gesendete Nachricht (für Edits).
type MessageRef struct {
	Target    Target `json:"target"`
	MessageID string `json:"message_id"`
}

// Sender einer eingehenden Nachricht.
type Sender struct {
	PlatformUserID string `json:"platform_user_id"`
	Display        string `json:"display"`
	IsBot          bool   `json:"is_bot"`
}

// Attachment eines Inbound-Ereignisses.
type Attachment struct {
	Kind     string `json:"kind"` // image|audio|voice|file
	Name     string `json:"name"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
	URL      string `json:"url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	Data     []byte `json:"-"`
}

// InboundEvent ist das normalisierte Eingangsereignis (9.1).
type InboundEvent struct {
	Platform    string       `json:"platform"`
	BotID       string       `json:"bot_id"` // welcher Bot (bei einem Bot pro Fylgja)
	ChatID      string       `json:"chat_id"`
	ThreadID    string       `json:"thread_id"`
	MessageID   string       `json:"message_id"`
	Sender      Sender       `json:"sender"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
	ReplyTo     string       `json:"reply_to"`
	ReplyToBot  bool         `json:"reply_to_bot"`
	Mentions    []string     `json:"mentions"`
	MentionsBot bool         `json:"mentions_bot"`
	IsDM        bool         `json:"is_dm"`
	ReceivedAt  time.Time    `json:"received_at"`
	// Command: Slash-Command inkl. Argumente ("stop", "queue text").
	Command string `json:"command,omitempty"`
	Args    string `json:"args,omitempty"`
	// Callback: Button-Klick (Approval), signierte Daten.
	Callback   string `json:"callback,omitempty"`
	CallbackID string `json:"callback_id,omitempty"`
	Reaction   string `json:"reaction,omitempty"`
	Raw        any    `json:"-"`
}

// Block einer RichMessage.
type Block struct {
	Type    string     `json:"type"` // text|code|table|image|file|buttons|progress|quote
	Text    string     `json:"text,omitempty"`
	Lang    string     `json:"lang,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	URL     string     `json:"url,omitempty"`
	Name    string     `json:"name,omitempty"`
	Data    []byte     `json:"-"`
	Buttons []Button   `json:"buttons,omitempty"`
	Percent int        `json:"percent,omitempty"`
}

// Button (Approval-Karten).
type Button struct {
	Label string `json:"label"`
	Data  string `json:"data,omitempty"` // signierte Callback-Daten
	URL   string `json:"url,omitempty"`  // Deep-Link (Step-up in Web-UI)
	Style string `json:"style,omitempty"` // primary|danger|secondary
}

// RichMessage ist plattformneutral.
type RichMessage struct {
	Blocks []Block `json:"blocks"`
}

// Text ist eine Hilfsfunktion.
func Text(s string) RichMessage { return RichMessage{Blocks: []Block{{Type: "text", Text: s}}} }

// ApprovalCard (9.6).
type ApprovalCard struct {
	ID        string    `json:"id"`
	DotName   string    `json:"dot_name"`
	Tool      string    `json:"tool"`
	Summary   string    `json:"summary"`
	Class     string    `json:"class"`
	Risk      string    `json:"risk"`
	Reason    string    `json:"reason"`
	Rationale string    `json:"rationale"`
	Preview   string    `json:"preview"`
	ExpiresAt time.Time `json:"expires_at"`
	StepUp    bool      `json:"step_up"`
	DeepLink  string    `json:"deep_link"`
	// Signierte Callback-Daten für die Buttons.
	ApproveData, DenyData, AlwaysData string
	// Status nach Entscheidung (Karte wird aktualisiert).
	Resolved string `json:"resolved,omitempty"`
}

// Health eines Adapters.
type Health struct {
	OK      bool      `json:"ok"`
	Detail  string    `json:"detail"`
	Since   time.Time `json:"since"`
}

// InboundHandler verarbeitet eingehende Ereignisse.
type InboundHandler func(ctx context.Context, ev InboundEvent)

// Channel ist das Adapter-Interface (9.1).
type Channel interface {
	Platform() string
	ID() string // Bot-ID (ein Bot pro Fylgja möglich)
	Capabilities() Capabilities
	Start(ctx context.Context, h InboundHandler) error
	Send(ctx context.Context, to Target, m RichMessage) (MessageRef, error)
	Edit(ctx context.Context, ref MessageRef, m RichMessage) error
	React(ctx context.Context, ref MessageRef, emoji string) error
	Typing(ctx context.Context, to Target) error
	SendApproval(ctx context.Context, to Target, a ApprovalCard) (MessageRef, error)
	UpdateApproval(ctx context.Context, ref MessageRef, a ApprovalCard) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	Download(ctx context.Context, a Attachment) ([]byte, error)
	Health() Health
}
