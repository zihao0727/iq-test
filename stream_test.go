package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamResponses(t *testing.T) {
	completed := `data: {"type":"response.completed","response":{"status":"completed","usage":{"output_tokens":73},"output":[{"type":"message","content":[{"type":"output_text","text":"<html><svg>完整</svg></html>"}]}]}}` + "\n\n"
	delta := `data: {"type":"response.output_text.delta","delta":"<html><svg>部分</svg></html>"}` + "\n\n"
	for _, tt := range []struct {
		name, body string
		ok         bool
	}{
		{"completed", ": heartbeat\r\n\r\nevent: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n" + delta + completed, true},
		{"final_without_separator", strings.TrimSpace(completed), true},
		{"multiline", "data: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\"}}\n\n", true},
		{"truncated", delta, false},
		{"done_without_completion", delta + "data: [DONE]\n\n", false},
		{"failed", delta + "data: {\"type\":\"response.failed\"}\n\n", false},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", false},
		{"error", "data: {\"type\":\"error\",\"message\":\"secret\"}\n\n", false},
		{"invalid", "data: {broken}\n\n", false},
		{"oversized", "data: " + strings.Repeat("x", 4*maxResponseBytes) + "\n\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(tt.body))}
			result, err := readResponse(resp)
			if (err == nil) != tt.ok {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if tt.name == "completed" && result.Usage.OutputTokens != 73 {
				t.Fatal("missing final usage")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("upstream error leaked")
			}
		})
	}
}

func TestPelicanStreamingExecution(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["stream"] != true || r.Header.Get("Accept") != "text/event-stream" {
			t.Error("streaming not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": heartbeat\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"<html><svg>secret\"}\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"</svg></html>\"}\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"output_tokens\":17}}}\n\n")
	}))
	defer upstream.Close()
	result := executeResponse(context.Background(), upstream.Client(), upstream.URL, "secret", "", record{Kind: "pelican"})
	if result.Status != "generated" || result.Output != "<html><svg>[REDACTED]</svg></html>" || result.OutputTokens != 17 {
		t.Fatalf("result=%+v", result)
	}
}
