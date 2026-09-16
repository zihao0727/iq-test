package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testApp(t *testing.T, endpoint string) *app {
	t.Helper()
	a, err := newApp(config{Endpoint: endpoint, Key: "test-secret-api-key", AdminToken: "test-admin-token", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEndpointNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, base := range []string{"https://www.sevnx.lol", "https://www.sevnx.lol/v1/", "https://www.sevnx.lol/v1/responses"} {
		if err := os.WriteFile(path, []byte("api_base_url: "+base+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := loadConfigFile(path)
		if err != nil || c.Endpoint != "https://www.sevnx.lol/v1/responses" {
			t.Fatalf("%s: %+v %v", base, c, err)
		}
	}
	for _, bad := range []string{"http://example.com", "https://user:pass@example.com", "ftp://example.com", "https://example.com?key=secret"} {
		if err := os.WriteFile(path, []byte("api_base_url: "+bad+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfigFile(path); err == nil {
			t.Fatalf("accepted insecure URL %s", bad)
		}
	}
}

func TestCandyScoring(t *testing.T) {
	for _, s := range []string{"21", "21颗", "答案：21。", "**21**", "答案是21个", "21\n", "最少取出21个糖果，因为……"} {
		if !candyPass(s) {
			t.Errorf("rejected %q", s)
		}
	}
	for _, s := range []string{"31", "2 1", "答案是二十一", ""} {
		if candyPass(s) {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestCandyMinimumByExhaustiveEnumeration(t *testing.T) {
	// Enumerate all no-pair draws. Shape counts that never occur in these draws
	// guarantee a match, regardless of the unknown flavors.
	var bad [25][18]bool
	for ar := 0; ar <= 7; ar++ {
		for pr := 0; pr <= 9; pr++ {
			for wr := 0; wr <= 8; wr++ {
				for as := 0; as <= 7; as++ {
					for ps := 0; ps <= 6; ps++ {
						for ws := 0; ws <= 4; ws++ {
							if !(ar > 0 && ps > 0 || pr > 0 && as > 0) {
								bad[ar+pr+wr][as+ps+ws] = true
							}
						}
					}
				}
			}
		}
	}
	minimum := 42
	for round := 0; round <= 24; round++ {
		for star := 0; star <= 17; star++ {
			if !bad[round][star] && round+star < minimum {
				minimum = round + star
			}
		}
	}
	if minimum != 21 || bad[9][12] {
		t.Fatalf("minimum=%d, 9+12 invalid=%v", minimum, bad[9][12])
	}
}

func TestResponsesContractAndParallelRound(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != "POST" {
			t.Error("wrong endpoint or method")
		}
		if r.Header.Get("Authorization") != "Bearer test-secret-api-key" {
			t.Error("missing API key")
		}
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p["model"] != model || p["stream"] != true || p["store"] != false {
			t.Errorf("incorrect payload: %v", p)
		}
		if p["reasoning"].(map[string]any)["effort"] != "low" {
			t.Error("wrong reasoning effort")
		}
		if _, ok := p["tools"]; ok {
			t.Error("tools must not be enabled")
		}
		output := "21"
		if p["input"] == pelicanPrompt {
			output = "```html\n<html><svg></svg><p>test-secret-api-key</p></html>\n```"
		} else if p["input"] != candyPrompt {
			t.Error("prompt changed")
		}
		mu.Lock()
		calls++
		mu.Unlock()
		jsonReply(w, 200, map[string]any{"status": "completed", "output": []any{map[string]any{"type": "reasoning"}, map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": output}}}}, "usage": map[string]int{"output_tokens": 42}})
	}))
	defer upstream.Close()
	a := testApp(t, upstream.URL+"/v1/responses")
	if !a.start(context.Background()) {
		t.Fatal("round not started")
	}
	waitRound(t, a)
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 2 {
		t.Fatalf("calls=%d", n)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.records) != 2 || a.records[0].Status != "pass" || a.records[1].Status != "generated" {
		t.Fatalf("records=%+v", a.records)
	}
	if strings.Contains(a.records[1].Output, a.cfg.Key) {
		t.Error("key leaked")
	}
	if a.records[0].OutputTokens != 42 {
		t.Error("usage missing")
	}
	if remaining := time.Until(a.next); remaining < 19*time.Minute || remaining > 20*time.Minute {
		t.Error("next run not scheduled")
	}
	b, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "records.json"))
	if err != nil || strings.Contains(string(b), a.cfg.Key) {
		t.Fatal("persistence missing or leaked secret", err)
	}
}

func waitRound(t *testing.T, a *app) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.RLock()
		running := a.running
		a.mu.RUnlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("round did not finish")
}

func TestCandyRetries(t *testing.T) {
	for _, tc := range []struct {
		name, kind, wantStatus string
		passAt, wantCalls      int
		httpError, cancel      bool
	}{
		{name: "first pass", kind: "candy", passAt: 1, wantCalls: 1, wantStatus: "pass"},
		{name: "retry pass", kind: "candy", passAt: 2, wantCalls: 2, wantStatus: "pass"},
		{name: "last retry pass", kind: "candy", passAt: 4, wantCalls: 4, wantStatus: "pass"},
		{name: "wrong answer limit", kind: "candy", wantCalls: 4, wantStatus: "fail"},
		{name: "request error limit", kind: "candy", httpError: true, wantCalls: 4, wantStatus: "error"},
		{name: "pelican no retry", kind: "pelican", wantCalls: 1, wantStatus: "fail"},
		{name: "canceled no retry", kind: "candy", cancel: true, wantCalls: 1, wantStatus: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.cancel {
					cancel()
					return
				}
				if tc.httpError {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				output := "31"
				if calls == tc.passAt {
					output = "21"
				}
				jsonReply(w, 200, map[string]any{
					"status": "completed",
					"output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": output}}}},
					"usage":  map[string]int{"output_tokens": 7},
				})
			}))
			a := testApp(t, server.URL)
			input := record{ID: "retry-test", Kind: tc.kind, Started: time.Now()}
			result := a.execute(ctx, input)
			server.Close()
			if calls != tc.wantCalls || result.Status != tc.wantStatus {
				t.Fatalf("calls=%d, result=%+v", calls, result)
			}
			if result.ID != input.ID || !result.Started.Equal(input.Started) {
				t.Fatal("retry changed record identity")
			}
			if !tc.httpError && !tc.cancel && result.OutputTokens != calls*7 {
				t.Fatalf("total tokens=%d, want %d", result.OutputTokens, calls*7)
			}
			if result.Status == "pass" && (result.Output != "21" || result.Error != "") {
				t.Fatalf("stale retry output: %+v", result)
			}
		})
	}
}

func TestErrorsAndNoRedirect(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"unauthorized", "test-secret-api-key", 401},
		{"invalid", "not json", 200},
		{"incomplete", `{"status":"incomplete","output":[]}`, 200},
		{"empty", `{"status":"completed","output":[]}`, 200},
		{"oversize", strings.Repeat("a", 2*1024*1024+1), 200},
		{"redirect", "", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.invalid/stolen")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			a := testApp(t, server.URL)
			r := a.execute(context.Background(), record{Kind: "candy"})
			if r.Status != "error" || strings.Contains(r.Error, a.cfg.Key) {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestAuthPublicDataAndSandbox(t *testing.T) {
	a := testApp(t, "https://example.invalid/v1/responses")
	a.records = []record{{ID: "abc", Kind: "pelican", Status: "generated", Output: "<html><svg></svg><script>fetch('/api/run')</script></html>"}}
	h := a.handler(context.Background())
	for _, token := range []string{"", "wrong", "test-admin-token"} {
		req := httptest.NewRequest("POST", "/api/run", nil)
		req.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("unauthorized request accepted: %d", w.Code)
		}
	}
	for _, path := range []string{"/api/status", "/api/records/abc", "/", "/app.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), a.cfg.Key) || strings.Contains(w.Body.String(), a.cfg.AdminToken) {
			t.Error("secret in public response")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/art/abc", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "connect-src 'none'") || strings.Contains(csp, "allow-same-origin") {
		t.Fatal("unsafe preview policy", csp)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/status", nil))
	if strings.Contains(w.Body.String(), "<script>") {
		t.Error("status should omit outputs")
	}
}

func TestBusyAndMissingKey(t *testing.T) {
	a := testApp(t, "https://example.invalid")
	a.cfg.Key = ""
	if a.start(context.Background()) {
		t.Error("started without key")
	}
	a.cfg.Key = "test-secret-api-key"
	a.running = true
	if a.start(context.Background()) {
		t.Error("overlapping round started")
	}
	req := httptest.NewRequest("POST", "/api/run", nil)
	req.Header.Set("Authorization", "Bearer test-admin-token")
	w := httptest.NewRecorder()
	a.handler(context.Background()).ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestRestartAndCorruptStore(t *testing.T) {
	a := testApp(t, "https://example.invalid")
	a.records = []record{{ID: "interrupted", Status: "running"}}
	a.persistLocked()
	restored, err := newApp(a.cfg)
	if err != nil || restored.records[0].Status != "error" {
		t.Fatal("restart recovery failed", err)
	}
	if err = os.WriteFile(filepath.Join(a.cfg.DataDir, "records.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = newApp(a.cfg); err == nil {
		t.Fatal("corrupt data silently discarded")
	}
}

func TestRetention(t *testing.T) {
	a := testApp(t, "http://127.0.0.1:1")
	for i := 0; i < 600; i++ {
		a.records = append(a.records, record{ID: fmt.Sprint(i), Status: "pass"})
	}
	a.start(context.Background())
	waitRound(t, a)
	if len(a.records) != 200 {
		t.Fatal("retention failed")
	}
}

func TestArtworkRetentionAndStartup(t *testing.T) {
	a := testApp(t, "http://127.0.0.1:1")
	var rows []record
	for i := 0; i < 150; i++ {
		rows = append(rows, record{ID: fmt.Sprintf("art-%d", i), Kind: "pelican", Status: "generated"})
	}
	for i := 0; i < 150; i++ {
		rows = append(rows, record{ID: fmt.Sprintf("test-%d", i), Kind: "candy", Status: "pass"})
	}
	// Simulate an existing store created before the retention limits.
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.DataDir, "records.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.records) != 200 || restored.records[99].ID != "art-99" || restored.records[100].ID != "test-0" || restored.records[199].ID != "test-99" {
		t.Fatal("startup did not preserve the newest records within both limits")
	}
	restored.records = append([]record{{ID: "new-art", Kind: "pelican", Status: "running"}}, restored.records...)
	restored.persistLocked()
	restored.records[0].Status = "generated"
	restored.persistLocked()
	count := 0
	for _, row := range restored.records {
		if row.Kind == "pelican" && row.Status == "generated" {
			count++
		}
		if row.ID == "art-99" {
			t.Fatal("oldest artwork was not removed after generation completed")
		}
	}
	if count != 100 {
		t.Fatalf("artworks=%d", count)
	}
	reloaded, err := newApp(a.cfg)
	if err != nil || len(reloaded.records) != len(restored.records) {
		t.Fatal("trimmed records were not persisted", err)
	}
}
