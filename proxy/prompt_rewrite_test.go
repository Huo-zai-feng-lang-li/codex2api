package proxy

import (
	"encoding/json"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/security/upstreamguard"
	"github.com/tidwall/gjson"
)

func TestApplyResponsesRequestSystemPromptOverridesInstructions(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","instructions":"old","system":"legacy","developer":"dev","input":"hello"}`)

	got, changed, err := applyResponsesRequestSystemPrompt(raw, "new system prompt")
	if err != nil {
		t.Fatalf("applyResponsesRequestSystemPrompt returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if instructions := gjson.GetBytes(got, "instructions").String(); instructions != "new system prompt" {
		t.Fatalf("instructions = %q, want rewritten prompt; body=%s", instructions, got)
	}
	for _, field := range []string{"system", "developer"} {
		if gjson.GetBytes(got, field).Exists() {
			t.Fatalf("%s should be removed from rewritten Responses request: %s", field, got)
		}
	}
}

func TestApplyChatRequestSystemPromptReplacesSystemAndDeveloperMessages(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"old system"},{"role":"developer","content":"old developer"},{"role":"user","content":"hello"}]}`)

	got, changed, err := applyChatRequestSystemPrompt(raw, "new system prompt")
	if err != nil {
		t.Fatalf("applyChatRequestSystemPrompt returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	messages := gjson.GetBytes(got, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2 after system/developer collapse; body=%s", len(messages), got)
	}
	if role := messages[0].Get("role").String(); role != "system" {
		t.Fatalf("first role = %q, want system; body=%s", role, got)
	}
	if content := messages[0].Get("content").String(); content != "new system prompt" {
		t.Fatalf("first content = %q, want rewritten prompt; body=%s", content, got)
	}
	if role := messages[1].Get("role").String(); role != "user" {
		t.Fatalf("second role = %q, want original user; body=%s", role, got)
	}
}

func TestApplyAnthropicRequestSystemPromptOverridesSystem(t *testing.T) {
	raw := []byte(`{"model":"claude-sonnet-4-6","system":"old system","messages":[{"role":"user","content":"hello"}]}`)

	got, changed, err := applyAnthropicRequestSystemPrompt(raw, "new system prompt")
	if err != nil {
		t.Fatalf("applyAnthropicRequestSystemPrompt returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if system := gjson.GetBytes(got, "system").String(); system != "new system prompt" {
		t.Fatalf("system = %q, want rewritten prompt; body=%s", system, got)
	}
}

func TestRewriteNonStreamResponsesBodyReplacesVisibleTextAndKeepsMetadata(t *testing.T) {
	raw := []byte(`{"id":"resp_1","model":"gpt-5.4","output_text":"old","usage":{"input_tokens":7},"output":[{"type":"message","content":[{"type":"output_text","text":"old"}]}]}`)

	got, changed, err := rewriteResponsesResponseBody(raw, "safe replacement")
	if err != nil {
		t.Fatalf("rewriteResponsesResponseBody returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if id := gjson.GetBytes(got, "id").String(); id != "resp_1" {
		t.Fatalf("id changed to %q; body=%s", id, got)
	}
	if text := gjson.GetBytes(got, "output_text").String(); text != "safe replacement" {
		t.Fatalf("output_text = %q, want replacement; body=%s", text, got)
	}
	if text := gjson.GetBytes(got, "output.0.content.0.text").String(); text != "safe replacement" {
		t.Fatalf("output content text = %q, want replacement; body=%s", text, got)
	}
}

func TestRewriteChatCompletionsResponseBodyReplacesChoiceContent(t *testing.T) {
	raw := []byte(`{"id":"chatcmpl_1","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"old"}}],"usage":{"total_tokens":3}}`)

	got, changed, err := rewriteChatCompletionsResponseBody(raw, "safe replacement")
	if err != nil {
		t.Fatalf("rewriteChatCompletionsResponseBody returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if content := gjson.GetBytes(got, "choices.0.message.content").String(); content != "safe replacement" {
		t.Fatalf("content = %q, want replacement; body=%s", content, got)
	}
	if id := gjson.GetBytes(got, "id").String(); id != "chatcmpl_1" {
		t.Fatalf("id changed to %q; body=%s", id, got)
	}
}

func TestRewriteAnthropicResponseReplacesTextBlock(t *testing.T) {
	raw := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"old"}],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)

	var response anthropicResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal anthropic response: %v", err)
	}
	changed := rewriteAnthropicResponseText(&response, "safe replacement")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	got, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal anthropic response: %v", err)
	}
	if text := gjson.GetBytes(got, "content.0.text").String(); text != "safe replacement" {
		t.Fatalf("text = %q, want replacement; body=%s", text, got)
	}
	if id := gjson.GetBytes(got, "id").String(); id != "msg_1" {
		t.Fatalf("id changed to %q; body=%s", id, got)
	}
}

func TestPromptRewriteSkipsEmptyPromptAndErrorResponses(t *testing.T) {
	if got, changed, err := applyResponsesRequestSystemPrompt([]byte(`{"model":"gpt-5.4","input":"hello"}`), " "); err != nil || changed || string(got) == "" {
		t.Fatalf("empty request prompt got changed=%t err=%v body=%s", changed, err, got)
	}
	rawError := []byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`)
	if got, changed, err := rewriteResponsesResponseBody(rawError, "safe replacement"); err != nil || changed || string(got) != string(rawError) {
		t.Fatalf("error response should be unchanged: changed=%t err=%v body=%s", changed, err, got)
	}
}

func TestPromptRewriteWorksWhenUpstreamGuardDisabled(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{
		UpstreamGuardMode:                upstreamguard.ModeOff,
		ProxyRequestSystemPromptEnabled:  true,
		ProxyRequestSystemPrompt:         "request guardrail",
		ProxyResponseRewriteEnabled:      true,
		ProxyResponseRewritePrompt:       "response replacement",
	})
	handler := NewHandler(store, nil, nil, nil)

	requestBody := handler.applyResponsesRequestPromptRewrite([]byte(`{"model":"gpt-5.4","instructions":"old","input":"hello"}`))
	if instructions := gjson.GetBytes(requestBody, "instructions").String(); instructions != "request guardrail" {
		t.Fatalf("instructions = %q, want request prompt rewrite with upstream guard off; body=%s", instructions, requestBody)
	}

	responseBody := handler.rewriteResponsesBodyForDownstream([]byte(`{"id":"resp_1","output_text":"old","output":[{"type":"message","content":[{"type":"output_text","text":"old"}]}]}`))
	if text := gjson.GetBytes(responseBody, "output_text").String(); text != "response replacement" {
		t.Fatalf("output_text = %q, want response rewrite with upstream guard off; body=%s", text, responseBody)
	}
	if text := gjson.GetBytes(responseBody, "output.0.content.0.text").String(); text != "response replacement" {
		t.Fatalf("output content text = %q, want response rewrite with upstream guard off; body=%s", text, responseBody)
	}
}
