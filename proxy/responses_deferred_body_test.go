package proxy

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestLazyOpenAIResponsesBodyDefersCachesAndInvalidatesPreparation(t *testing.T) {
	deferred := newLazyOpenAIResponsesBody([]byte(`{
		"model":"gpt-5.4",
		"reasoning_effort":"high",
		"input":[{"role":"user","content":"hello"}]
	}`))
	if deferred.prepared != nil {
		t.Fatal("constructor prepared the OpenAI body eagerly")
	}

	first := deferred.Bytes()
	if effort := gjson.GetBytes(first, "reasoning.effort").String(); effort != "high" {
		t.Fatalf("reasoning.effort = %q, want high; body=%s", effort, first)
	}
	if len(first) == 0 || len(deferred.prepared) == 0 {
		t.Fatal("Bytes did not cache the prepared body")
	}
	second := deferred.Bytes()
	if &first[0] != &second[0] {
		t.Fatal("repeated Bytes call rebuilt the prepared body")
	}

	deferred.Reset([]byte(`{"model":"gpt-5.4","input":"replacement"}`))
	if deferred.prepared != nil {
		t.Fatal("Reset did not invalidate the prepared body")
	}
	if input := gjson.GetBytes(deferred.Bytes(), "input").String(); input != "replacement" {
		t.Fatalf("input = %q, want replacement", input)
	}

	prepared := []byte(`{"model":"gpt-5.4","input":"already-prepared"}`)
	deferred.SetPrepared(prepared)
	if got := deferred.Bytes(); &got[0] != &prepared[0] {
		t.Fatal("SetPrepared did not preserve the supplied body")
	}
}
