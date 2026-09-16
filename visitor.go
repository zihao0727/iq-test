package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type visitorJob struct {
	ID      string    `json:"id"`
	Owner   string    `json:"-"`
	Peer    string    `json:"-"`
	Created time.Time `json:"created"`
	Running bool      `json:"running"`
	Records []record  `json:"records"`
}

var excludedNetworks = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range excludedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func visitorEndpoint(raw string) (string, error) {
	if len(raw) > 2048 {
		return "", errors.New("URL 过长")
	}
	endpoint, err := normalizeEndpoint(raw)
	if err != nil {
		return "", errors.New("请输入有效 API URL，不允许用户名、查询参数或片段")
	}
	u, _ := url.Parse(endpoint)
	if u.Scheme != "https" {
		return "", errors.New("访客自测仅支持 HTTPS 公网地址")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") {
		return "", errors.New("不允许访问本机或内网地址")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicIP(ip) {
		return "", errors.New("不允许访问本机、内网或保留地址")
	}
	return endpoint, nil
}

func newVisitorClient() *http.Client {
	transport := &http.Transport{
		Proxy: nil, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: upstreamTimeout,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("DNS resolution failed")
		}
		// Validate every DNS answer and dial a validated IP directly. TLS still
		// verifies the original hostname; redirects and environment proxies are off.
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, errors.New("non-public destination blocked")
			}
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			err = dialErr
		}
		return nil, err
	}
	return &http.Client{
		Transport: transport, Timeout: upstreamTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func randomID() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func visitorOwner(r *http.Request) string {
	cookie, err := r.Cookie("sevnx_visitor")
	if err != nil || len(cookie.Value) != 48 {
		return ""
	}
	if _, err := hex.DecodeString(cookie.Value); err != nil {
		return ""
	}
	return cookie.Value
}

func (a *app) pruneVisitorsLocked() {
	for id, job := range a.visitors {
		if !job.Running && time.Since(job.Created) > time.Hour {
			delete(a.visitors, id)
		}
	}
}

func (a *app) visitorRoutes(mux *http.ServeMux, serverCtx context.Context) {
	mux.HandleFunc("POST /api/visitor", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			jsonReply(w, 415, map[string]string{"error": "请求必须为 JSON"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				jsonReply(w, 403, map[string]string{"error": "不允许跨站发起检测"})
				return
			}
		}
		var input struct {
			Endpoint string `json:"endpoint"`
			Key      string `json:"key"`
			Kind     string `json:"kind"`
			Consent  bool   `json:"consent"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 12*1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			jsonReply(w, 400, map[string]string{"error": "请求格式无效或过大"})
			return
		}
		endpoint, err := visitorEndpoint(input.Endpoint)
		if err != nil {
			jsonReply(w, 400, map[string]string{"error": err.Error()})
			return
		}
		input.Key = strings.TrimSpace(input.Key)
		if len(input.Key) < 8 || len(input.Key) > 4096 || strings.ContainsAny(input.Key, "\r\n") {
			jsonReply(w, 400, map[string]string{"error": "请填写有效的 API Key（8 至 4096 字符）"})
			return
		}
		if !input.Consent || (input.Kind != "both" && input.Kind != "candy" && input.Kind != "pelican") {
			jsonReply(w, 400, map[string]string{"error": "请选择检测项目并确认调用费用"})
			return
		}
		if serverCtx.Err() != nil {
			jsonReply(w, 503, map[string]string{"error": "服务正在关闭"})
			return
		}
		owner := visitorOwner(r)
		if owner == "" {
			owner = randomID()
		}
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		a.visitorMu.Lock()
		a.pruneVisitorsLocked()
		active, peerActive := 0, 0
		var last time.Time
		for _, job := range a.visitors {
			if job.Running {
				active++
				if job.Peer == peer {
					peerActive++
				}
			}
			if job.Peer == peer && job.Created.After(last) {
				last = job.Created
			}
		}
		if active >= 4 || peerActive >= 2 || len(a.visitors) >= 64 || time.Since(last) < 3*time.Second {
			a.visitorMu.Unlock()
			w.Header().Set("Retry-After", "5")
			jsonReply(w, 429, map[string]string{"error": "自测请求较多，请稍后重试"})
			return
		}
		job := &visitorJob{ID: randomID(), Owner: owner, Peer: peer, Created: time.Now(), Running: true}
		kinds := []string{input.Kind}
		if input.Kind == "both" {
			kinds = []string{"candy", "pelican"}
		}
		for _, kind := range kinds {
			job.Records = append(job.Records, record{ID: randomID(), Kind: kind, Status: "running", Started: time.Now()})
		}
		a.visitors[job.ID] = job
		a.visitorMu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: "sevnx_visitor", Value: owner, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
			Secure: r.TLS != nil || strings.HasPrefix(r.Header.Get("Origin"), "https://"), MaxAge: 3600,
		})
		jsonReply(w, 202, map[string]string{"id": job.ID})
		go a.runVisitor(serverCtx, job.ID, endpoint, input.Key)
	})
	mux.HandleFunc("GET /api/visitor/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.visitorMu.Lock()
		defer a.visitorMu.Unlock()
		a.pruneVisitorsLocked()
		job := a.visitors[r.PathValue("id")]
		if job == nil || job.Owner != visitorOwner(r) {
			http.NotFound(w, r)
			return
		}
		jsonReply(w, 200, job)
	})
	mux.HandleFunc("GET /visitor-art/{id}/{kind}", func(w http.ResponseWriter, r *http.Request) {
		a.visitorMu.Lock()
		defer a.visitorMu.Unlock()
		a.pruneVisitorsLocked()
		job := a.visitors[r.PathValue("id")]
		if job == nil || job.Owner != visitorOwner(r) {
			http.NotFound(w, r)
			return
		}
		for _, row := range job.Records {
			if row.Kind == r.PathValue("kind") && row.Kind == "pelican" && row.Status == "generated" {
				writeArt(w, row.Output)
				return
			}
		}
		http.NotFound(w, r)
	})
}

func (a *app) runVisitor(ctx context.Context, id, endpoint, key string) {
	a.visitorMu.Lock()
	rows := append([]record(nil), a.visitors[id].Records...)
	a.visitorMu.Unlock()
	var wg sync.WaitGroup
	for i, row := range rows {
		wg.Add(1)
		go func(i int, row record) {
			defer wg.Done()
			result := executeResponse(ctx, a.visitorClient, endpoint, key, "", row)
			a.visitorMu.Lock()
			a.visitors[id].Records[i] = result
			a.visitorMu.Unlock()
		}(i, row)
	}
	wg.Wait()
	a.visitorMu.Lock()
	a.visitors[id].Running = false
	a.visitorMu.Unlock()
}

func writeArt(w http.ResponseWriter, output string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'")
	_, _ = io.WriteString(w, output)
	preview, _ := assets.ReadFile("web/preview.js")
	_, _ = io.WriteString(w, "<script>"+string(preview)+"</script>")
}
