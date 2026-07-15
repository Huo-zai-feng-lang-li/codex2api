package proxy

import (
	"encoding/json"
	"strings"
)

func normalizePromptText(prompt string) string {
	return strings.TrimSpace(prompt)
}

func applyResponsesRequestSystemPrompt(raw []byte, prompt string) ([]byte, bool, error) {
	prompt = normalizePromptText(prompt)
	if prompt == "" {
		return raw, false, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, err
	}
	body["instructions"] = prompt
	delete(body, "system")
	delete(body, "developer")
	out, err := json.Marshal(body)
	return out, err == nil, err
}

func (h *Handler) applyResponsesRequestPromptRewrite(raw []byte) []byte {
	if h == nil || h.store == nil {
		return raw
	}
	cfg := h.store.GetPromptRewriteConfig()
	if !cfg.RequestSystemPromptEnabled {
		return raw
	}
	rewritten, _, err := applyResponsesRequestSystemPrompt(raw, cfg.RequestSystemPrompt)
	if err != nil {
		return raw
	}
	return rewritten
}

func applyChatRequestSystemPrompt(raw []byte, prompt string) ([]byte, bool, error) {
	prompt = normalizePromptText(prompt)
	if prompt == "" {
		return raw, false, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, err
	}
	rawMessages, _ := body["messages"].([]any)
	messages := make([]any, 0, len(rawMessages)+1)
	messages = append(messages, map[string]any{
		"role":    "system",
		"content": prompt,
	})
	for _, item := range rawMessages {
		msg, ok := item.(map[string]any)
		if !ok {
			messages = append(messages, item)
			continue
		}
		role := strings.ToLower(strings.TrimSpace(toString(msg["role"])))
		if role == "system" || role == "developer" {
			continue
		}
		messages = append(messages, item)
	}
	body["messages"] = messages
	out, err := json.Marshal(body)
	return out, err == nil, err
}

func (h *Handler) applyChatRequestPromptRewrite(raw []byte) []byte {
	if h == nil || h.store == nil {
		return raw
	}
	cfg := h.store.GetPromptRewriteConfig()
	if !cfg.RequestSystemPromptEnabled {
		return raw
	}
	rewritten, _, err := applyChatRequestSystemPrompt(raw, cfg.RequestSystemPrompt)
	if err != nil {
		return raw
	}
	return rewritten
}

func applyAnthropicRequestSystemPrompt(raw []byte, prompt string) ([]byte, bool, error) {
	prompt = normalizePromptText(prompt)
	if prompt == "" {
		return raw, false, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, err
	}
	body["system"] = prompt
	out, err := json.Marshal(body)
	return out, err == nil, err
}

func (h *Handler) applyAnthropicRequestPromptRewrite(raw []byte) []byte {
	if h == nil || h.store == nil {
		return raw
	}
	cfg := h.store.GetPromptRewriteConfig()
	if !cfg.RequestSystemPromptEnabled {
		return raw
	}
	rewritten, _, err := applyAnthropicRequestSystemPrompt(raw, cfg.RequestSystemPrompt)
	if err != nil {
		return raw
	}
	return rewritten
}

func rewriteResponsesResponseBody(raw []byte, prompt string) ([]byte, bool, error) {
	prompt = normalizePromptText(prompt)
	if prompt == "" {
		return raw, false, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, err
	}
	if _, isError := body["error"]; isError {
		return raw, false, nil
	}
	changed := false
	if _, ok := body["output_text"]; ok {
		body["output_text"] = prompt
		changed = true
	}
	if output, ok := body["output"].([]any); ok {
		for _, item := range output {
			if rewriteResponsesOutputItemText(item, prompt) {
				changed = true
			}
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(body)
	return out, err == nil, err
}

func (h *Handler) rewriteResponsesBodyForDownstream(raw []byte) []byte {
	if h == nil || h.store == nil {
		return raw
	}
	cfg := h.store.GetPromptRewriteConfig()
	if !cfg.ResponseRewriteEnabled {
		return raw
	}
	rewritten, _, err := rewriteResponsesResponseBody(raw, cfg.ResponseRewritePrompt)
	if err != nil {
		return raw
	}
	return rewritten
}

func rewriteResponsesOutputItemText(item any, prompt string) bool {
	obj, ok := item.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	if objType := strings.ToLower(strings.TrimSpace(toString(obj["type"]))); objType == "message" {
		if content, ok := obj["content"].([]any); ok {
			for _, part := range content {
				partObj, ok := part.(map[string]any)
				if !ok {
					continue
				}
				partType := strings.ToLower(strings.TrimSpace(toString(partObj["type"])))
				if partType == "output_text" || partType == "text" {
					partObj["text"] = prompt
					changed = true
				}
			}
		}
	}
	return changed
}

func rewriteChatCompletionsResponseBody(raw []byte, prompt string) ([]byte, bool, error) {
	prompt = normalizePromptText(prompt)
	if prompt == "" {
		return raw, false, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, err
	}
	if _, isError := body["error"]; isError {
		return raw, false, nil
	}
	changed := false
	if choices, ok := body["choices"].([]any); ok {
		for _, choice := range choices {
			choiceObj, ok := choice.(map[string]any)
			if !ok {
				continue
			}
			msg, ok := choiceObj["message"].(map[string]any)
			if !ok {
				continue
			}
			if _, ok := msg["content"]; ok {
				msg["content"] = prompt
				changed = true
			}
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(body)
	return out, err == nil, err
}

func (h *Handler) rewriteChatBodyForDownstream(raw []byte) []byte {
	if h == nil || h.store == nil {
		return raw
	}
	cfg := h.store.GetPromptRewriteConfig()
	if !cfg.ResponseRewriteEnabled {
		return raw
	}
	rewritten, _, err := rewriteChatCompletionsResponseBody(raw, cfg.ResponseRewritePrompt)
	if err != nil {
		return raw
	}
	return rewritten
}

func (h *Handler) rewriteAnthropicResponseForDownstream(response *anthropicResponse) bool {
	if h == nil || h.store == nil {
		return false
	}
	cfg := h.store.GetPromptRewriteConfig()
	if !cfg.ResponseRewriteEnabled {
		return false
	}
	return rewriteAnthropicResponseText(response, cfg.ResponseRewritePrompt)
}

func rewriteAnthropicResponseText(response *anthropicResponse, prompt string) bool {
	if response == nil {
		return false
	}
	prompt = normalizePromptText(prompt)
	if prompt == "" {
		return false
	}
	changed := false
	for i := range response.Content {
		if strings.EqualFold(strings.TrimSpace(response.Content[i].Type), "text") {
			response.Content[i].Text = prompt
			changed = true
		}
	}
	return changed
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
