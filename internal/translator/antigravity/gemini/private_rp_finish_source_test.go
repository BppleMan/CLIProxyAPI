package gemini

import (
	"context"
	"github.com/tidwall/gjson"
	"testing"
)

func TestPrivateRPFinishProvenance(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	ctx := context.WithValue(context.Background(), "alt", "")
	var state any
	raw := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"fixture","thoughtSignature":"signed"}]},"finishReason":"STOP"}]}}`)
	got := ConvertAntigravityResponseToGemini(ctx, "", nil, nil, raw, &state)[0]
	if gjson.GetBytes(got, "candidates.0.cpaFinishReasonSource").String() != "provider" || gjson.GetBytes(got, "candidates.0.content.parts.0.thoughtSignature").String() != "signed" {
		t.Fatalf("native source/signature lost: %s", got)
	}
	state = nil
	ConvertAntigravityResponseToGemini(ctx, "", nil, nil, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}`), &state)
	got = ConvertAntigravityResponseToGemini(ctx, "", nil, nil, []byte("[DONE]"), &state)[0]
	if gjson.GetBytes(got, "candidates.0.cpaFinishReasonSource").String() != "synthetic_done" {
		t.Fatalf("synthetic stream origin lost: %s", got)
	}
	got = ConvertAntigravityResponseToGeminiNonStream(ctx, "", nil, nil, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}`), nil)
	if gjson.GetBytes(got, "candidates.0.cpaFinishReasonSource").String() != "synthetic_nonstream" {
		t.Fatalf("synthetic nonstream origin lost: %s", got)
	}
	t.Setenv("CPA_PRIVATE_RP", "0")
	state = nil
	got = ConvertAntigravityResponseToGemini(ctx, "", nil, nil, raw, &state)[0]
	if gjson.GetBytes(got, "candidates.0.cpaFinishReasonSource").Exists() {
		t.Fatal("mode-off response changed")
	}
}
