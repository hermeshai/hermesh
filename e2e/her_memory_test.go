package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestHerMemory: Her keeps what the owner tells her to remember
// (docs/her-memory.md), on an environment that is not this Mac, reached as
// the apps reach it. She notes a line without asking the owner to approve
// anything; the note is a file on her Her Server; and after the owner clears
// her conversation, as Her's settings do, the new conversation starts with
// what she kept. Her's settings read the memory and clear it.
func TestHerMemory(t *testing.T) {
	env := newHerEnvironment(t)
	const line = "- Replies are always in Chinese. (2026-11-11)"
	const recall = "[[mock:recall]] What do you remember about me?"

	askHer(t, env, recall, "什么都不记得。")
	if asked, _ := askHer(t, env, "[[mock:remember:"+line+"]] From now on, reply in Chinese.", "记下了。"); len(asked) != 0 {
		t.Fatalf("she asked the owner to approve %v", asked)
	}
	file := filepath.Join(env.home, "bots", env.bot.ID, "memory", "MEMORY.md")
	if kept, err := env.onMachine("cat", file); err != nil || strings.TrimSpace(string(kept)) != line {
		t.Fatalf("%s on the machine: %q, %v", file, kept, err)
	}

	// The same call the apps make from Her's settings.
	herCall(t, env, "", "bots/clear", map[string]string{"bot_id": env.bot.ID})
	if _, said := askHer(t, env, recall, "记得："); said != "记得："+line {
		t.Fatalf("after clearing she said %q", said)
	}

	// Her's settings show the memory as written, and clear it; she forgets
	// at once, in the same conversation.
	var kept struct {
		Text string `json:"text"`
		Path string `json:"path"`
	}
	json.Unmarshal(herCall(t, env, "", "bots/memory", map[string]string{"bot_id": env.bot.ID}), &kept)
	if strings.TrimSpace(kept.Text) != line || filepath.Base(kept.Path) != "MEMORY.md" {
		t.Fatalf("bots/memory: %+v", kept)
	}
	herCall(t, env, "", "bots/memory_clear", map[string]string{"bot_id": env.bot.ID})
	if _, err := env.onMachine("test", "-e", file); err == nil {
		t.Fatalf("%s is still there", file)
	}
	askHer(t, env, recall, "什么都不记得。")
}
