package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

const model = "gpt-6-astra"
const interval = 20 * time.Minute
const upstreamTimeout = 10 * time.Minute
const candyMaxRetries = 3
const pelicanPrompt = `生成一个可直接打开的 HTML 文件，使用 SVG 绘制鹈鹕骑自行车的 2D 动画，通过 CSS 或 JavaScript 实现车轮转动、双腿踩踏、身体轻微起伏、围巾随风摆动，以及背景缓慢移动，动作自然协调。
整体采用清新治愈的复古绘本风格：低饱和配色、手绘感线条、简洁平涂、充足留白，可加入轻微纸张质感。
鹈鹕和自行车必须分别拥有独立、明确的配色，不能仅用轮廓线表现，也不能与背景融为一体。鹈鹕的羽毛、嘴、腿和配饰应有分区填色；自行车的车架、轮胎、轮圈、车把与坐垫也应有各自的颜色。两者的主色应明显区分，同时与整体画面协调。颜色在动画过程中保持稳定，不随机闪变。
鹈鹕骑自行车是固定主题，其余细节保留适度随机性：每次生成时自行选择骑行环境、天气、时段、角色配饰，以及鹈鹕、自行车和背景的配色，让它们共同构成协调的小故事，避免机械拼凑。画面温柔、有趣，但不幼稚。
页面搭配一句与场景呼应的简短中文标题和少量氛围文案，并提供动画暂停／继续按钮。布局适配桌面和手机，主体清晰完整，避免元素重叠。
所有图形均用 SVG 绘制，不使用外部图片、字体或依赖。不要读取或参考任何本地文件，不需要运行测试。直接生成完整 HTML 文件。`
const candyPrompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求，不许联网，直接告诉我答案）

          苹果味 桃子味 西瓜味
圆形       7      9      8
五角星形   7      6      4`

type config struct {
	Addr, Endpoint, Key, AdminToken, DataDir string
}

func loadConfig() (config, error) {
	return loadConfigFile("config.yaml")
}

func loadConfigFile(path string) (config, error) {
	var file struct {
		ListenAddr string `yaml:"listen_addr"`
		APIBaseURL string `yaml:"api_base_url"`
		APIKey     string `yaml:"api_key"`
		AdminToken string `yaml:"admin_token"`
		DataDir    string `yaml:"data_dir"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &file); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	c := config{Addr: file.ListenAddr, Endpoint: file.APIBaseURL, Key: strings.TrimSpace(file.APIKey), AdminToken: strings.TrimSpace(file.AdminToken), DataDir: file.DataDir}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8090"
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://www.sevnx.lol"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	endpoint, err := normalizeEndpoint(c.Endpoint)
	if err != nil {
		return c, err
	}
	u, _ := url.Parse(endpoint)
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return c, errors.New("API_BASE_URL must use HTTPS (except loopback)")
	}
	c.Endpoint = endpoint
	return c, nil
}

func normalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("API URL must not include credentials, query or fragment")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("API URL must use HTTPS")
	}
	p := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(p, "/v1/responses") {
		u.Path = p
	} else if strings.HasSuffix(p, "/v1") {
		u.Path = p + "/responses"
	} else {
		u.Path = p + "/v1/responses"
	}
	return u.String(), nil
}

type record struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Started      time.Time `json:"started"`
	DurationMS   int64     `json:"durationMs"`
	OutputTokens int       `json:"outputTokens"`
	Output       string    `json:"output,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type app struct {
	cfg           config
	client        *http.Client
	mu            sync.RWMutex
	records       []record
	running       bool
	next          time.Time
	storageError  bool
	visitorMu     sync.Mutex
	visitors      map[string]*visitorJob
	visitorClient *http.Client
}

func newApp(c config) (*app, error) {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return nil, err
	}
	a := &app{cfg: c, client: &http.Client{
		Timeout:       upstreamTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, records: []record{}, visitors: make(map[string]*visitorJob), visitorClient: newVisitorClient()}
	b, err := os.ReadFile(filepath.Join(c.DataDir, "records.json"))
	if err == nil {
		if err = json.Unmarshal(b, &a.records); err != nil {
			return nil, fmt.Errorf("invalid records.json: %w", err)
		}
		for i := range a.records {
			if a.records[i].Status == "running" {
				a.records[i].Status = "error"
				a.records[i].Error = "服务重启，检测中断"
			}
		}
		a.persistLocked()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return a, nil
}

func (a *app) persistLocked() {
	a.trimRecordsLocked()
	b, err := json.Marshal(a.records)
	path := filepath.Join(a.cfg.DataDir, "records.json")
	if err == nil {
		err = os.WriteFile(path+".tmp", b, 0600)
	}
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}
	a.storageError = err != nil
	if err != nil {
		log.Print("Unable to persist records; check DATA_DIR permissions and disk space")
	}
}

func (a *app) trimRecordsLocked() {
	// Records are stored newest first; preserve that order while enforcing both caps.
	kept := make([]record, 0, 200)
	artworks := 0
	for _, row := range a.records {
		if row.Kind == "pelican" && row.Status == "generated" {
			if artworks >= 100 {
				continue
			}
			artworks++
		}
		kept = append(kept, row)
		if len(kept) == 200 {
			break
		}
	}
	a.records = kept
}

func (a *app) start(ctx context.Context) bool {
	a.mu.Lock()
	if a.running || a.cfg.Key == "" || ctx.Err() != nil {
		a.mu.Unlock()
		return false
	}
	a.running = true
	a.next = time.Now().Add(interval)
	var pending []record
	for _, kind := range []string{"candy", "pelican"} {
		var id [12]byte
		_, _ = rand.Read(id[:])
		pending = append(pending, record{ID: hex.EncodeToString(id[:]), Kind: kind, Status: "running", Started: time.Now()})
	}
	a.records = append(pending, a.records...)
	a.persistLocked()
	a.mu.Unlock()
	go func() {
		var wg sync.WaitGroup
		for _, r := range pending {
			wg.Add(1)
			go func(r record) {
				defer wg.Done()
				result := a.execute(ctx, r)
				a.mu.Lock()
				for i := range a.records {
					if a.records[i].ID == r.ID {
						a.records[i] = result
						break
					}
				}
				a.persistLocked()
				a.mu.Unlock()
			}(r)
		}
		wg.Wait()
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
	}()
	return true
}

func (a *app) schedule(ctx context.Context) {
	if a.cfg.Key == "" {
		return
	}
	a.start(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.mu.RLock()
			due := !a.next.After(time.Now()) && !a.running
			a.mu.RUnlock()
			if due {
				a.start(ctx)
			}
		}
	}
}

type responseBody struct {
	StreamText string `json:"-"`
	Status     string `json:"status"`
	Output     []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

var answerPattern = regexp.MustCompile(`21`)

func candyPass(s string) bool { return answerPattern.MatchString(s) }

func extractHTML(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
			if j := strings.LastIndex(s, "```"); j >= 0 {
				s = s[:j]
			}
		}
	}
	lower := strings.ToLower(s)
	start := strings.Index(lower, "<!doctype html")
	if start < 0 {
		start = strings.Index(lower, "<html")
	}
	if start >= 0 {
		s = s[start:]
	}
	if end := strings.LastIndex(strings.ToLower(s), "</html>"); end >= 0 {
		s = s[:end+7]
	}
	return strings.TrimSpace(s)
}

func (a *app) execute(ctx context.Context, r record) record {
	return executeResponse(ctx, a.client, a.cfg.Endpoint, a.cfg.Key, a.cfg.AdminToken, r)
}

func executeResponse(ctx context.Context, client *http.Client, endpoint, key, adminToken string, r record) record {
	start := time.Now()
	totalTokens := 0
	for attempt := 0; ; attempt++ {
		result := executeResponseOnce(ctx, client, endpoint, key, adminToken, r)
		totalTokens += result.OutputTokens
		result.OutputTokens = totalTokens
		result.DurationMS = time.Since(start).Milliseconds()
		if r.Kind != "candy" || result.Status == "pass" || attempt >= candyMaxRetries || ctx.Err() != nil {
			return result
		}
	}
}

func executeResponseOnce(ctx context.Context, client *http.Client, endpoint, key, adminToken string, r record) record {
	start := time.Now()
	prompt, maxTokens := candyPrompt, 8192
	if r.Kind == "pelican" {
		prompt, maxTokens = pelicanPrompt, 16000
	}
	payload := map[string]any{
		"model": model, "input": prompt, "reasoning": map[string]string{"effort": "low"},
		"max_output_tokens": maxTokens, "store": false, "stream": true,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(b)))
	if err != nil {
		r.Status, r.Error = "error", "API 地址无效"
		return r
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		r.Status, r.Error = "error", "上游连接失败或超时，请检查服务端网络和配置"
		r.DurationMS = time.Since(start).Milliseconds()
		return r
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.DurationMS = time.Since(start).Milliseconds()
		r.Status, r.Error = "error", fmt.Sprintf("上游 HTTP %d，请检查 API Key、模型权限或额度", resp.StatusCode)
		return r
	}
	result, readErr := readResponse(resp)
	r.DurationMS = time.Since(start).Milliseconds()
	if readErr != nil {
		r.Status, r.Error = "error", readErr.Error()
		return r
	}
	var text strings.Builder
	for _, out := range result.Output {
		if out.Type != "message" {
			continue
		}
		for _, c := range out.Content {
			if c.Type == "output_text" {
				text.WriteString(c.Text)
			}
		}
	}
	r.Output = text.String()
	if r.Output == "" {
		r.Output = result.StreamText
	}
	if key != "" {
		r.Output = strings.ReplaceAll(r.Output, key, "[REDACTED]")
	}
	if adminToken != "" {
		r.Output = strings.ReplaceAll(r.Output, adminToken, "[REDACTED]")
	}
	r.OutputTokens = result.Usage.OutputTokens
	if strings.TrimSpace(r.Output) == "" {
		r.Status, r.Error = "error", "上游未返回文本"
		return r
	}
	if r.Kind == "candy" {
		r.Status = "fail"
		if candyPass(r.Output) {
			r.Status = "pass"
		}
	} else {
		r.Output = extractHTML(r.Output)
		r.Status = "generated"
		if !strings.Contains(strings.ToLower(r.Output), "<svg") || !strings.Contains(strings.ToLower(r.Output), "<html") {
			r.Status, r.Error = "fail", "输出未包含 HTML 与 SVG"
		}
	}
	return r
}

func jsonReply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (a *app) handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	a.visitorRoutes(mux, ctx)
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		rows := make([]record, len(a.records))
		for i, row := range a.records {
			row.Output = ""
			rows[i] = row
		}
		jsonReply(w, 200, map[string]any{
			"model": model, "effort": "low", "protocol": "/v1/responses",
			"endpoint":   a.cfg.Endpoint,
			"configured": a.cfg.Key != "", "running": a.running,
			"next": a.next, "serverTime": time.Now(), "intervalSeconds": int(interval.Seconds()),
			"manualEnabled": a.cfg.AdminToken != "", "storageError": a.storageError,
			"records": rows,
		})
	})
	mux.HandleFunc("POST /api/run", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || a.cfg.AdminToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.AdminToken)) != 1 {
			jsonReply(w, 401, map[string]string{"error": "管理员令牌无效或未配置"})
			return
		}
		if !a.start(ctx) {
			jsonReply(w, 409, map[string]string{"error": "已有检测运行中，或服务端尚未配置 API_KEY"})
			return
		}
		jsonReply(w, 202, map[string]string{"status": "started"})
	})
	mux.HandleFunc("GET /api/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		for _, row := range a.records {
			if row.ID == r.PathValue("id") {
				jsonReply(w, 200, row)
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /art/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		for _, row := range a.records {
			if row.ID != r.PathValue("id") || row.Kind != "pelican" || row.Status != "generated" {
				continue
			}
			writeArt(w, row.Output)
			return
		}
		http.NotFound(w, r)
	})
	root, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(root)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}

func main() {
	c, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	a, err := newApp(c)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: c.Addr, Handler: a.handler(ctx), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	listener, err := net.Listen("tcp", c.Addr)
	if err != nil {
		log.Fatal(err)
	}
	go a.schedule(ctx)
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("SEVNX Lab listening on %s; API configured: %s", c.Addr, strconv.FormatBool(c.Key != ""))
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	// Let canceled upstream requests finish persisting their final state.
	for {
		a.mu.RLock()
		running := a.running
		a.mu.RUnlock()
		if !running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}
