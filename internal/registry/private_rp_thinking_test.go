package registry

import "testing"

func TestPrivateRPThinkingDiscovery(t *testing.T) {
	r := &ModelRegistry{}
	m := &ModelInfo{ID: "synthetic", Thinking: &ThinkingSupport{Min: 1024, Max: 8192, DynamicAllowed: true, Levels: []string{"low", "high"}}}
	t.Setenv("CPA_PRIVATE_RP", "0")
	if _, ok := r.convertModelToMap(m, "gemini")["thinkingSupport"]; ok {
		t.Fatal("mode-off discovery changed")
	}
	t.Setenv("CPA_PRIVATE_RP", "1")
	got := r.convertModelToMap(m, "gemini")["thinkingSupport"].(*ThinkingSupport)
	if got.Min != 1024 || got.Max != 8192 || !got.DynamicAllowed || len(got.Levels) != 2 {
		t.Fatal("thinking capabilities lost")
	}
	got.Levels[0] = "mutated"
	if m.Thinking.Levels[0] != "low" {
		t.Fatal("discovery exposes mutable registry slice")
	}
}
