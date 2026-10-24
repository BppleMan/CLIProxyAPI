package chat_completions

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPrivateRPRejectsMidSystemAndPreservesPrefix(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	_, err := ConvertOpenAIRequestToAntigravity("gemini-3.1-pro", []byte(`{"messages":[{"role":"user","content":"start"},{"role":"system","content":"secret mid instruction"}]}`), false)
	if err == nil || strings.Contains(err.Error(), "secret mid") {
		t.Fatal("missing safe explicit rejection")
	}
	out, err := ConvertOpenAIRequestToAntigravity("gemini-3.1-pro", []byte(`{"messages":[{"role":"system","content":"MAIN"},{"role":"developer","content":"SECOND"},{"role":"user","content":"go"}],"stop":"END"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "request.systemInstruction.parts.#").Int() != 2 || gjson.GetBytes(out, "request.systemInstruction.parts.1.text").String() != "SECOND" {
		t.Fatal("prefix changed")
	}
}
