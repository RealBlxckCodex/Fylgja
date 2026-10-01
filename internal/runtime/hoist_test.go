package runtime

import (
	"strings"
	"testing"

	"github.com/realblxckcodex/fylgja/internal/llm"
)

func TestHoistImages(t *testing.T) {
	tm := func(id, img string) llm.Message {
		m := llm.Message{Role: llm.Tool, ToolCallID: id, Name: "desktop.screenshot", Content: "ok"}
		if img != "" {
			m.Images = []string{img}
		}
		return m
	}
	msgs := []llm.Message{
		{Role: llm.User, Content: "los"},
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "1"}, {ID: "2"}}},
		tm("1", "data:image/jpeg;base64,AAA"), tm("2", ""),
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "3"}}},
		tm("3", "data:image/jpeg;base64,BBB"),
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "4"}}},
		tm("4", "data:image/jpeg;base64,CCC"),
	}
	out := hoistImages(msgs)
	var imgs []string
	for i, m := range out {
		if m.Role == llm.Tool && len(m.Images) > 0 {
			t.Fatalf("tool-nachricht %d trägt noch Bilder", i)
		}
		if m.Role == llm.User {
			imgs = append(imgs, m.Images...)
		}
		// Eine Assistant-Nachricht mit Tool-Calls muss von allen Tool-Antworten gefolgt werden, bevor etwas anderes kommt.
		if m.Role == llm.Assistant && len(m.ToolCalls) > 0 {
			for j := range m.ToolCalls {
				if out[i+1+j].Role != llm.Tool {
					t.Fatalf("tool-antworten unterbrochen bei %d", i)
				}
			}
		}
	}
	if len(imgs) != 2 || !strings.HasSuffix(imgs[0], "BBB") || !strings.HasSuffix(imgs[1], "CCC") {
		t.Fatalf("nur die letzten zwei bilder sollten bleiben: %v", imgs)
	}
	if !strings.Contains(out[2].Content, "Bild entfernt") {
		t.Fatalf("älteres bild nicht ersetzt: %q", out[2].Content)
	}
	if len(msgs[2].Images) != 1 {
		t.Fatal("eingabe wurde verändert")
	}
}
