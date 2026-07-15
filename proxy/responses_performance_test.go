package proxy

import "testing"

var benchmarkResponsesBodySink []byte

func BenchmarkPrepareResponsesBody(b *testing.B) {
	rawBody := benchmarkResponsesRequestBody()
	b.ReportAllocs()
	b.SetBytes(int64(len(rawBody)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		body, _ := PrepareResponsesBody(rawBody)
		benchmarkResponsesBodySink = body
	}
}

func BenchmarkPrepareOpenAIResponsesBody(b *testing.B) {
	rawBody := benchmarkResponsesRequestBody()
	b.ReportAllocs()
	b.SetBytes(int64(len(rawBody)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		benchmarkResponsesBodySink = PrepareOpenAIResponsesBody(rawBody)
	}
}

func benchmarkResponsesRequestBody() []byte {
	return []byte(`{
		"model":"gpt-5.4",
		"stream":true,
		"reasoning_effort":"high",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"Summarize the latency profile and preserve every contract field."}]},
			{"type":"additional_tools","tools":[{"type":"custom","name":"future_tool","description":"future contract"}]},
			{"type":"function_call","call_id":"call_1","name":"lookup_metrics","arguments":"{\"window\":\"5m\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"p50\":12,\"p95\":31}"}
		],
		"tools":[
			{"type":"function","name":"lookup_metrics","description":"Read latency metrics","parameters":{"type":"object","properties":{"window":{"type":"string"}},"required":["window"],"additionalProperties":false}},
			{"type":"function","name":"trace_request","description":"Trace one request","parameters":{"type":"object","properties":{"request_id":{"type":"string"}},"required":["request_id"],"additionalProperties":false}},
			{"type":"web_search_preview"}
		],
		"text":{"format":{"type":"json_schema","name":"latency_report","schema":{"type":"object","properties":{"summary":{"type":"string"},"p50":{"type":"number"},"p95":{"type":"number"}},"required":["summary","p50","p95"],"additionalProperties":false}}},
		"metadata":{"request_id":"bench-request","tenant":"local"}
	}`)
}
