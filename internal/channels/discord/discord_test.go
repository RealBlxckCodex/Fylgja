package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestConvertMessage(t *testing.T) {
	b := &Bot{id: "BOT"}
	ev, ok := b.convertMessage(&discordgo.Message{ID: "m1", ChannelID: "c1", GuildID: "g1", Content: "<@BOT> fasse das zusammen",
		Author: &discordgo.User{ID: "u1", Username: "sam"}, Mentions: []*discordgo.User{{ID: "BOT"}},
		Attachments: []*discordgo.MessageAttachment{{Filename: "voice-message.ogg", ContentType: "audio/ogg", URL: "https://cdn.discordapp.com/x"}}})
	if !ok || !ev.MentionsBot || ev.IsDM || ev.Text != "fasse das zusammen" || ev.Attachments[0].Kind != "voice" {
		t.Fatalf("%+v", ev)
	}
	if _, ok := b.convertMessage(&discordgo.Message{Author: &discordgo.User{ID: "BOT"}}); ok {
		t.Fatal("eigene nachricht verarbeitet")
	}
}
