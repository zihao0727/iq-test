package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVisitorNetworkBoundaries(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.1.1", "172.16.1.1", "192.168.1.1", "169.254.169.254", "0.0.0.0", "100.64.1.1", "198.18.1.1", "192.0.2.1", "224.0.0.1", "240.0.0.1", "::1", "::ffff:127.0.0.1", "fd00::1", "fe80::1", "64:ff9b::a00:1", "2001:db8::1", "2002:7f00:1::"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("accepted nonpublic IP %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("blocked public IP %s", ip)
		}
	}
	for _, bad := range []string{"https://localhost", "https://LOCALHOST.", "https://127.0.0.1", "https://[::1]", "https://10.0.0.1", "http://example.com", "https://user:secret@example.com", "https://example.com?key=secret", "https://example.com/#secret", "https://[fe80::1%25eth0]", "file:///etc/passwd"} {
		if _, err := visitorEndpoint(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if endpoint, err := visitorEndpoint("https://custom.example/v1/"); err != nil || endpoint != "https://custom.example/v1/responses" {
		t.Fatal(endpoint, err)
	}
	client := newVisitorClient()
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Error("environment proxy must be disabled")
	}
	// The TCP listener must never be reached, even when the hostname resolves.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	if conn, err := transport.DialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port)); err == nil {
		conn.Close()
		t.Fatal("loopback DNS bypassed destination validation")
	}
}

func postVisitor(h http.Handler, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "https://lab.example/api/visitor", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://lab.example")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestVisitorPrivateRound(t *testing.T) {
	a := testApp(t, "https://default.example/v1/responses")
	var calls atomic.Int32
	a.visitorClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://custom.example/v1/responses" {
			t.Errorf("wrong visitor URL: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer visitor-secret" {
			t.Error("visitor key not used")
		}
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p["model"] != model || p["reasoning"].(map[string]any)["effort"] != "low" {
			t.Error("model contract changed")
		}
		if p["stream"] != true {
			t.Error("visitor streaming contract changed")
		}
		output := "21"
		if p["input"] == pelicanPrompt {
			output = "<html><svg></svg><p>visitor-secret</p></html>"
		}
		b, _ := json.Marshal(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": output}}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: make(http.Header)}, nil
	})}
	h := a.handler(context.Background())
	w := postVisitor(h, `{"endpoint":"https://custom.example","key":"visitor-secret","kind":"both","consent":true}`, nil)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var accepted map[string]string
	json.Unmarshal(w.Body.Bytes(), &accepted)
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	id := accepted["id"]
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.visitorMu.Lock()
		running := a.visitors[id].Running
		a.visitorMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("visitor timed out")
		}
		time.Sleep(time.Millisecond)
	}
	for _, path := range []string{"/api/visitor/" + id, "/visitor-art/" + id + "/pelican"} {
		req := httptest.NewRequest("GET", "https://lab.example"+path, nil)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Error("visitor output accessible without session")
		}
		req.AddCookie(cookie)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("owner cannot access: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "visitor-secret") || strings.Contains(w.Body.String(), cookie.Value) {
			t.Error("secret exposed")
		}
	}
	req := httptest.NewRequest("GET", "https://lab.example/api/status", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), id) || len(a.records) != 0 || calls.Load() != 2 {
		t.Fatal("visitor mixed into automatic records")
	}
	if _, err := os.Stat(filepath.Join(a.cfg.DataDir, "records.json")); !os.IsNotExist(err) {
		t.Error("visitor results persisted")
	}
	if a.cfg.Key != "test-secret-api-key" || a.cfg.Endpoint != "https://default.example/v1/responses" {
		t.Fatal("default credentials changed")
	}
}

func TestVisitorValidationLimitsAndExpiry(t *testing.T) {
	a := testApp(t, "https://example.invalid")
	h := a.handler(context.Background())
	for _, body := range []string{
		`{"endpoint":"https://example.com","key":"short","kind":"both","consent":true}`,
		`{"endpoint":"https://example.com","key":"valid-secret","kind":"both","consent":false}`,
		`{"endpoint":"https://example.com","key":"valid-secret","kind":"arbitrary","consent":true}`,
		`{"endpoint":"https://example.com","key":"valid-secret","kind":"both","consent":true,"model":"other"}`,
		`{"endpoint":"https://127.0.0.1","key":"valid-secret","kind":"both","consent":true}`,
		`{} {}`,
		strings.Repeat("x", 13000),
	} {
		if w := postVisitor(h, body, nil); w.Code != 400 {
			t.Errorf("unexpected status %d for %s", w.Code, body[:min(len(body), 80)])
		}
	}
	req := httptest.NewRequest("POST", "https://lab.example/api/visitor", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatal("non-JSON accepted")
	}
	req = httptest.NewRequest("POST", "https://lab.example/api/visitor", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-site POST accepted")
	}
	for i := 0; i < 4; i++ {
		id := randomID()
		a.visitors[id] = &visitorJob{ID: id, Running: true}
	}
	w = postVisitor(h, `{"endpoint":"https://example.com","key":"valid-secret","kind":"both","consent":true}`, nil)
	if w.Code != 429 {
		t.Fatal("global concurrency limit bypassed")
	}
	a.visitors["expired"] = &visitorJob{Created: time.Now().Add(-2 * time.Hour)}
	a.pruneVisitorsLocked()
	if a.visitors["expired"] != nil {
		t.Fatal("expired result not removed")
	}
}
