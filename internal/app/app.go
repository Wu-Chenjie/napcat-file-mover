package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"napcat-file-mover/internal/bot"
	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/downloader"
	"napcat-file-mover/internal/napcat"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/search"
	"napcat-file-mover/internal/security"
	"napcat-file-mover/internal/storage"
	"napcat-file-mover/internal/worker"
)

type Core struct {
	cfg       *config.Config
	repo      *repository.SQLite
	napcat    *napcat.Client
	gateway   *bot.Gateway
	workers   *worker.Pool
	server    *http.Server
	sessions  map[string]time.Time
	searches  map[string][]repository.SearchResult
	sessionMu sync.Mutex
	searchMu  sync.Mutex
}

func New(cfg *config.Config) (*Core, error) {
	repo, err := repository.OpenSQLite(cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.NapCat.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	nc := napcat.New(cfg.NapCat.Endpoint, cfg.NapCat.Token, timeout, cfg.NapCat.MaxConcurrentRequests)
	maxBytes := cfg.Website.MaxFileSizeMB * 1024 * 1024
	dl := downloader.New(cfg.Website.UserAgent, maxBytes, cfg.Worker.BufferSizeKB*1024)
	st := storage.NewLocal(cfg.Storage.LocalRoot)
	pool := worker.New(cfg, repo, nc, dl, st)
	return &Core{
		cfg:      cfg,
		repo:     repo,
		napcat:   nc,
		gateway:  bot.NewGateway(cfg, repo, nc),
		workers:  pool,
		sessions: map[string]time.Time{},
		searches: map[string][]repository.SearchResult{},
	}, nil
}

func (c *Core) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	c.routes(mux)
	c.server = &http.Server{Addr: c.cfg.Server.Listen, Handler: mux}
	c.workers.Start(ctx)
	go func() {
		if err := c.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server: %v", err)
		}
	}()
	log.Printf("NapCatFileMover listening on http://%s", c.cfg.Server.Listen)
	return nil
}

func (c *Core) Stop(ctx context.Context) {
	c.workers.Stop()
	if c.server != nil {
		_ = c.server.Shutdown(ctx)
	}
	if c.repo != nil {
		_ = c.repo.Close()
	}
}

func (c *Core) routes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", c.health)
	mux.HandleFunc("/readyz", c.ready)
	mux.HandleFunc("/metrics", c.metrics)
	mux.HandleFunc("/onebot/event", c.onebotEvent)
	mux.HandleFunc("/api/auth/login", c.login)
	mux.HandleFunc("/api/auth/logout", c.requireAuth(c.logout))
	mux.HandleFunc("/api/config", c.requireAuth(c.configAPI))
	mux.HandleFunc("/api/tasks", c.requireAuth(c.tasks))
	mux.HandleFunc("/api/tasks/", c.requireAuth(c.taskAction))
	mux.HandleFunc("/api/search/files", c.requireAuth(c.searchFiles))
	mux.HandleFunc("/api/search/", c.requireAuth(c.searchAction))
	mux.HandleFunc("/api/events", c.requireAuth(c.events))
	mux.HandleFunc("/", c.static)
}

func (c *Core) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Core) ready(w http.ResponseWriter, _ *http.Request) {
	checks := map[string]bool{
		"database": fileExists(c.cfg.Database.Path),
		"cache":    dirExists(c.cfg.Paths.CacheDir),
		"files":    dirExists(c.cfg.Storage.LocalRoot),
	}
	status := http.StatusOK
	for _, ok := range checks {
		if !ok {
			status = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, status, checks)
}

func (c *Core) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte("# HELP mover_up Whether the mover process is up\n# TYPE mover_up gauge\nmover_up 1\n"))
}

func (c *Core) onebotEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ev napcat.OneBotEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	go c.gateway.HandleEvent(context.Background(), ev, r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Core) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(c.cfg.App.AdminToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token, err := randomToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c.sessionMu.Lock()
	c.sessions[token] = time.Now().Add(24 * time.Hour)
	c.sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "mover_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(24 * time.Hour)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Core) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("mover_session")
	if cookie != nil {
		c.sessionMu.Lock()
		delete(c.sessions, cookie.Value)
		c.sessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "mover_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type publicConfig struct {
	App       publicAppConfig        `json:"app"`
	Server    config.ServerConfig    `json:"server"`
	NapCat    publicNapCatConfig     `json:"napcat"`
	Bot       config.BotConfig       `json:"bot"`
	Website   config.WebsiteConfig   `json:"website"`
	Storage   config.StorageConfig   `json:"storage"`
	Worker    config.WorkerConfig    `json:"worker"`
	RateLimit config.RateLimitConfig `json:"rate_limit"`
	Search    config.SearchConfig    `json:"search"`
	Paths     config.Paths           `json:"paths"`
}

type publicAppConfig struct {
	AdminTokenSet bool   `json:"admin_token_set"`
	AdminToken    string `json:"admin_token,omitempty"`
}

type publicNapCatConfig struct {
	Endpoint              string `json:"endpoint"`
	TokenSet              bool   `json:"token_set"`
	Token                 string `json:"token,omitempty"`
	TimeoutSeconds        int    `json:"timeout_seconds"`
	MaxConcurrentRequests int    `json:"max_concurrent_requests"`
	RetryMaxAttempts      int    `json:"retry_max_attempts"`
}

func (c *Core) configAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, c.publicConfig())
	case http.MethodPut:
		var req publicConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := c.updateConfig(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.Save(c.cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_required": true, "config": c.publicConfig()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *Core) publicConfig() publicConfig {
	return publicConfig{
		App:       publicAppConfig{AdminTokenSet: c.cfg.App.AdminToken != ""},
		Server:    c.cfg.Server,
		NapCat:    publicNapCatConfig{Endpoint: c.cfg.NapCat.Endpoint, TokenSet: c.cfg.NapCat.Token != "", TimeoutSeconds: c.cfg.NapCat.TimeoutSeconds, MaxConcurrentRequests: c.cfg.NapCat.MaxConcurrentRequests, RetryMaxAttempts: c.cfg.NapCat.RetryMaxAttempts},
		Bot:       c.cfg.Bot,
		Website:   c.cfg.Website,
		Storage:   c.cfg.Storage,
		Worker:    c.cfg.Worker,
		RateLimit: c.cfg.RateLimit,
		Search:    c.cfg.Search,
		Paths:     c.cfg.Paths,
	}
}

func (c *Core) updateConfig(req publicConfig) error {
	if req.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	if req.NapCat.Endpoint == "" {
		return errors.New("napcat.endpoint is required")
	}
	if req.Website.MaxFileSizeMB <= 0 {
		return errors.New("website.max_file_size_mb must be positive")
	}
	if req.Worker.MaxActiveTasks <= 0 || req.Worker.BufferSizeKB <= 0 || req.Worker.MaxRetries < 0 {
		return errors.New("worker settings are invalid")
	}
	if req.Search.HighConfidence <= 0 || req.Search.HighConfidence > 1 {
		return errors.New("search.high_confidence must be between 0 and 1")
	}
	if req.Search.MaxBatchFiles <= 0 || req.Search.MaxBatchSizeMB <= 0 {
		return errors.New("search batch limits must be positive")
	}
	c.cfg.Server = req.Server
	c.cfg.NapCat.Endpoint = req.NapCat.Endpoint
	c.cfg.NapCat.TimeoutSeconds = req.NapCat.TimeoutSeconds
	c.cfg.NapCat.MaxConcurrentRequests = req.NapCat.MaxConcurrentRequests
	c.cfg.NapCat.RetryMaxAttempts = req.NapCat.RetryMaxAttempts
	if req.NapCat.Token != "" {
		c.cfg.NapCat.Token = req.NapCat.Token
	}
	if req.App.AdminToken != "" {
		c.cfg.App.AdminToken = req.App.AdminToken
	}
	c.cfg.Bot = req.Bot
	c.cfg.Website = req.Website
	c.cfg.Storage = req.Storage
	c.cfg.Worker = req.Worker
	c.cfg.RateLimit = req.RateLimit
	c.cfg.Search = req.Search
	if c.cfg.Storage.LocalRoot == "" {
		c.cfg.Storage.LocalRoot = c.cfg.Paths.FilesDir
	}
	return nil
}

func (c *Core) tasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		groupID, _ := strconv.ParseInt(q.Get("group_id"), 10, 64)
		tasks, err := c.repo.ListTasks(r.Context(), repository.TaskFilter{
			Status: q.Get("status"), Type: q.Get("type"), Query: q.Get("q"), Limit: limit, Offset: offset, GroupID: groupID,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
	case http.MethodPost:
		var req repository.Task
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.IdempotencyKey == "" {
			req.IdempotencyKey = worker.Idempotency(req.SourceType, req.SourceURL, req.SourceFileID, req.TargetType, strconv.FormatInt(req.TargetGroupID, 10))
		}
		if req.FileName != "" {
			req.FileName = security.SanitizeFilename(req.FileName)
		}
		id, err := c.repo.CreateTask(r.Context(), &req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *Core) taskAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "bad task id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		t, err := c.repo.GetTask(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, t)
		return
	}
	if r.Method != http.MethodPost || len(parts) != 2 {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch parts[1] {
	case "retry":
		err = c.repo.RetryTask(r.Context(), id)
	case "pause":
		err = c.repo.SetTaskStatus(r.Context(), id, repository.StatusPaused, "")
	case "resume":
		err = c.repo.SetTaskStatus(r.Context(), id, repository.StatusPending, "")
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Core) searchFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	groupID, _ := strconv.ParseInt(r.URL.Query().Get("group_id"), 10, 64)
	indexed := search.BuildIndexedText(q)
	query := strings.TrimSpace(indexed.Normalized + " " + indexed.Pinyin + " " + indexed.Initials + " " + indexed.NGrams)
	results, err := c.repo.SearchFiles(r.Context(), query, groupID, r.URL.Query().Get("ext"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, _ := randomToken()
	c.searchMu.Lock()
	c.searches[id] = results
	c.searchMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"search_id": id, "results": results})
}

func (c *Core) searchAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/search/"), "/")
	if len(parts) != 2 || parts[1] != "transfer" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Target string `json:"target"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Target == "" {
		req.Target = "storage"
	}
	c.searchMu.Lock()
	results := append([]repository.SearchResult(nil), c.searches[parts[0]]...)
	c.searchMu.Unlock()
	if len(results) == 0 {
		http.Error(w, "search expired or empty", http.StatusNotFound)
		return
	}
	targetGroup := int64(0)
	targetType := "storage"
	taskType := repository.TaskQQToStorage
	if req.Target != "storage" {
		v, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			http.Error(w, "bad target", http.StatusBadRequest)
			return
		}
		targetGroup = v
		targetType = "qq"
		taskType = repository.TaskQQToQQ
	}
	created := 0
	total := int64(0)
	for _, item := range results {
		if item.Score < c.cfg.Search.HighConfidence {
			continue
		}
		total += item.FileSize
		if c.cfg.Search.MaxBatchSizeMB > 0 && total > c.cfg.Search.MaxBatchSizeMB*1024*1024 {
			break
		}
		task := &repository.Task{
			TaskType: taskType, SourceType: "qq", SourceGroupID: item.GroupID, SourceFileID: item.FileID, SourceBusID: item.BusID,
			TargetType: targetType, TargetGroupID: targetGroup, FileName: item.FileName, FileSize: item.FileSize,
			IdempotencyKey: worker.Idempotency("search", parts[0], item.FileID, req.Target), MaxRetries: c.cfg.Worker.MaxRetries,
		}
		id, err := c.repo.CreateTask(r.Context(), task)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if id != 0 {
			created++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created})
}

func (c *Core) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			tasks, _ := c.repo.ListTasks(r.Context(), repository.TaskFilter{Limit: 20})
			b, _ := json.Marshal(map[string]any{"tasks": tasks, "ts": time.Now().Unix()})
			_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

func (c *Core) static(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if path == "." || path == "" {
		path = "index.html"
	}
	dist := filepath.Join("frontend", "dist", path)
	if _, err := os.Stat(dist); err == nil {
		http.ServeFile(w, r, dist)
		return
	}
	if path != "index.html" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(fallbackHTML))
}

func (c *Core) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("mover_session")
		if err != nil || !c.validSession(cookie.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (c *Core) validSession(token string) bool {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	exp, ok := c.sessions[token]
	if !ok || time.Now().After(exp) {
		delete(c.sessions, token)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func randomToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

const fallbackHTML = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>NapCat File Mover</title></head>
<body><main style="font-family:system-ui;padding:32px;max-width:760px;margin:auto"><h1>NapCat File Mover</h1><p>GUI assets are not built yet. Run <code>npm install</code> and <code>npm run build</code> in <code>frontend/</code>, then restart.</p></main></body>
</html>`
