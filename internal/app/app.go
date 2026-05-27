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
	"net/url"
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
	"napcat-file-mover/internal/queue"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/search"
	"napcat-file-mover/internal/security"
	"napcat-file-mover/internal/storage"
	"napcat-file-mover/internal/worker"
)

type Core struct {
	cfg             *config.Config
	repo            repository.Store
	napcat          *napcat.Client
	gateway         *bot.Gateway
	workers         *worker.Pool
	storage         storage.Storage
	queue           *queue.RedisStream
	semantic        *search.OllamaClient
	vectorIndex     *search.VectorIndex
	semanticReady   bool
	semanticError   string
	restartRequired bool
	lastReloadAt    time.Time
	server          *http.Server
	wsListener      *napcat.WSListener
	sessions        map[string]time.Time
	searches        map[string][]repository.SearchResult
	sessionMu       sync.Mutex
	searchMu        sync.Mutex
	runtimeMu       sync.RWMutex
}

func New(cfg *config.Config) (*Core, error) {
	baseRepo, err := openRepository(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	var q *queue.RedisStream
	repo := baseRepo
	if cfg.Redis.Enabled {
		q, err = queue.NewRedisStream(context.Background(), cfg.Redis)
		if err != nil {
			_ = baseRepo.Close()
			return nil, err
		}
		repo = repository.NewQueuedStore(baseRepo, q)
	}
	timeout := time.Duration(cfg.NapCat.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	nc := napcat.New(cfg.NapCat.Endpoint, cfg.NapCat.Token, timeout, cfg.NapCat.MaxConcurrentRequests)
	maxBytes := cfg.Website.MaxFileSizeMB * 1024 * 1024
	dl := downloader.New(cfg.Website.UserAgent, maxBytes, cfg.Worker.BufferSizeKB*1024)
	st, err := openStorage(context.Background(), cfg)
	if err != nil {
		_ = repo.Close()
		if q != nil {
			_ = q.Close()
		}
		return nil, err
	}
	pool := worker.New(cfg, repo, nc, dl, st).WithQueue(q)
	core := &Core{
		cfg:      cfg,
		repo:     repo,
		napcat:   nc,
		gateway:  bot.NewGateway(cfg, repo, nc, st),
		storage:  st,
		queue:    q,
		workers:  pool,
		sessions: map[string]time.Time{},
		searches: map[string][]repository.SearchResult{},
	}
	core.configureSemantic(cfg)
	return core, nil
}

func (c *Core) configureSemantic(cfg *config.Config) {
	if cfg.Search.Semantic.Enabled && strings.EqualFold(cfg.Search.Semantic.Provider, "ollama") {
		c.semantic = search.NewOllamaClient(cfg.Search.Semantic)
		c.semanticError = ""
		return
	}
	c.semantic = nil
	c.vectorIndex = nil
	c.semanticReady = false
	c.semanticError = ""
}

func openRepository(ctx context.Context, cfg *config.Config) (repository.Store, error) {
	switch strings.ToLower(cfg.Database.Driver) {
	case "", "sqlite":
		return repository.OpenSQLite(cfg.Database.Path)
	case "postgres", "postgresql":
		return repository.OpenPostgres(ctx, cfg.Database.DSN)
	default:
		return nil, fmt.Errorf("unsupported database.driver %q", cfg.Database.Driver)
	}
}

func openStorage(ctx context.Context, cfg *config.Config) (storage.Storage, error) {
	switch strings.ToLower(cfg.Storage.Type) {
	case "", "local":
		return storage.NewLocal(cfg.Storage.LocalRoot), nil
	case "s3":
		return storage.NewS3(ctx, cfg.Storage.S3)
	default:
		return nil, fmt.Errorf("unsupported storage.type %q", cfg.Storage.Type)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func defaultWSURI(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	u.Scheme = "ws"
	port := u.Port()
	if port != "" {
		p, err := strconv.Atoi(port)
		if err == nil {
			u.Host = fmt.Sprintf("%s:%d", u.Hostname(), p+1)
		}
	}
	return u.String()
}

func (c *Core) indexLocalFiles() {
	lister, ok := c.storage.(storage.LocalFileLister)
	if c.storage == nil || !ok {
		return
	}
	files, err := lister.ListLocalFiles(context.Background())
	if err != nil {
		log.Printf("list local files: %v", err)
		return
	}
	for _, f := range files {
		idx := search.BuildIndexedText(f.Name)
		if err := c.repo.IndexLocalFile(context.Background(), repository.FileCatalog{
			GroupID:        0,
			FileID:         f.SHA256,
			BusID:          0,
			FileName:       f.Name,
			Ext:            strings.ToLower(filepath.Ext(f.Name)),
			FileSize:       f.Size,
			FolderPath:     f.Path,
			NormalizedText: idx.Normalized,
			Pinyin:         idx.Pinyin,
			Initials:       idx.Initials,
			NGrams:         idx.NGrams,
		}); err != nil {
			log.Printf("index local file %s: %v", f.Name, err)
		}
	}
	log.Printf("indexed %d local files", len(files))
}

func (c *Core) refreshSemanticIndex(ctx context.Context) {
	c.runtimeMu.RLock()
	client := c.semantic
	enabled := c.cfg.Search.Semantic.Enabled
	batchSize := c.cfg.Search.Semantic.BatchSize
	c.runtimeMu.RUnlock()
	if !enabled || client == nil {
		return
	}
	catalog, err := c.repo.ListCatalog(ctx, 10000)
	if err != nil {
		c.setSemanticState(nil, false, err.Error())
		log.Printf("semantic catalog load: %v", err)
		return
	}
	if batchSize <= 0 {
		batchSize = 16
	}
	for i := range catalog {
		if catalog[i].EmbeddingJSON != "" {
			continue
		}
		if i > 0 && i%batchSize == 0 {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		vec, err := client.Embed(ctx, semanticText(catalog[i]))
		if err != nil {
			c.setSemanticState(search.NewVectorIndex(catalog), len(catalog) > 0, err.Error())
			log.Printf("semantic embedding fallback to text search: %v", err)
			return
		}
		catalog[i].EmbeddingJSON = search.EncodeVector(vec)
		if catalog[i].ID != 0 {
			_ = c.repo.UpdateCatalogEmbedding(ctx, catalog[i].ID, catalog[i].EmbeddingJSON)
		}
	}
	c.setSemanticState(search.NewVectorIndex(catalog), true, "")
	log.Printf("semantic index loaded: %d catalog items", len(catalog))
}

func (c *Core) setSemanticState(idx *search.VectorIndex, ready bool, msg string) {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	c.vectorIndex = idx
	c.semanticReady = ready
	c.semanticError = msg
}

func semanticText(f repository.FileCatalog) string {
	return strings.TrimSpace(strings.Join([]string{
		f.FileName,
		f.FolderPath,
		f.NormalizedText,
		f.Pinyin,
		f.Initials,
	}, " "))
}

func (c *Core) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	c.routes(mux)
	c.server = &http.Server{Addr: c.cfg.Server.Listen, Handler: corsMiddleware(mux)}
	go func() {
		c.indexLocalFiles()
		c.refreshSemanticIndex(ctx)
	}()
	if c.queue != nil {
		go c.requeuePending(ctx)
	}
	if c.cfg.Reload.Enabled {
		go c.watchConfig(ctx)
	}
	c.workers.Start(ctx)
	go func() {
		if err := c.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server: %v", err)
		}
	}()
	log.Printf("NapCatFileMover listening on http://%s", c.cfg.Server.Listen)

	wsURI := c.cfg.NapCat.WSURI
	if wsURI == "" {
		wsURI = defaultWSURI(c.cfg.NapCat.Endpoint)
	}
	if wsURI != "" {
		c.wsListener = napcat.NewWSListener(wsURI, c.cfg.NapCat.Token, func(ev napcat.OneBotEvent) {
			c.gateway.HandleEvent(context.Background(), ev, "")
		})
		if err := c.wsListener.Connect(); err != nil {
			log.Printf("ws listener: %v (bot commands via HTTP /onebot/event still work)", err)
		}
	}
	return nil
}

func (c *Core) Stop(ctx context.Context) {
	c.workers.Stop()
	if c.wsListener != nil {
		_ = c.wsListener.Close()
	}
	if c.server != nil {
		_ = c.server.Shutdown(ctx)
	}
	if c.repo != nil {
		_ = c.repo.Close()
	}
	if c.queue != nil {
		_ = c.queue.Close()
	}
}

func (c *Core) requeuePending(ctx context.Context) {
	ids, err := c.repo.PendingTaskIDs(ctx, 10000)
	if err != nil {
		log.Printf("redis requeue pending: %v", err)
		return
	}
	for _, id := range ids {
		if err := c.queue.Enqueue(ctx, id); err != nil {
			log.Printf("redis enqueue pending task %d: %v", id, err)
		}
	}
	log.Printf("redis requeued %d pending tasks", len(ids))
}

func (c *Core) routes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", c.health)
	mux.HandleFunc("/readyz", c.ready)
	mux.HandleFunc("/metrics", c.metrics)
	mux.HandleFunc("/onebot/event", c.onebotEvent)
	mux.HandleFunc("/api/auth/login", c.login)
	mux.HandleFunc("/api/auth/logout", c.requireAuth(c.logout))
	mux.HandleFunc("/api/runtime/status", c.requireAuth(c.runtimeStatus))
	mux.HandleFunc("/api/config/reload", c.requireAuth(c.reloadConfig))
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
		"database": databaseReady(c.cfg),
		"cache":    dirExists(c.cfg.Paths.CacheDir),
		"storage":  storageReady(c.cfg),
	}
	status := http.StatusOK
	for _, ok := range checks {
		if !ok {
			status = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, status, checks)
}

func databaseReady(cfg *config.Config) bool {
	if strings.EqualFold(cfg.Database.Driver, "postgres") || strings.EqualFold(cfg.Database.Driver, "postgresql") {
		return cfg.Database.DSN != ""
	}
	return fileExists(cfg.Database.Path)
}

func storageReady(cfg *config.Config) bool {
	if strings.EqualFold(cfg.Storage.Type, "s3") {
		return cfg.Storage.S3.Bucket != ""
	}
	return dirExists(cfg.Storage.LocalRoot)
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
	log.Printf("[onebot] received: post_type=%s msg_type=%s group=%d user=%d raw=%s", ev.PostType, ev.MessageType, ev.GroupID, ev.UserID, ev.RawMessage)
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
	http.SetCookie(w, &http.Cookie{Name: "mover_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteNoneMode, Secure: false, Expires: time.Now().Add(24 * time.Hour)})
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
	Database  config.DatabaseConfig  `json:"database"`
	Redis     config.RedisConfig     `json:"redis"`
	Storage   publicStorageConfig    `json:"storage"`
	Worker    config.WorkerConfig    `json:"worker"`
	RateLimit config.RateLimitConfig `json:"rate_limit"`
	Search    config.SearchConfig    `json:"search"`
	Reload    config.ReloadConfig    `json:"reload"`
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

type publicStorageConfig struct {
	Type      string         `json:"type"`
	LocalRoot string         `json:"local_root"`
	S3        publicS3Config `json:"s3"`
}

type publicS3Config struct {
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	Region       string `json:"region"`
	AccessKey    string `json:"access_key"`
	SecretKeySet bool   `json:"secret_key_set"`
	SecretKey    string `json:"secret_key,omitempty"`
	UsePathStyle bool   `json:"use_path_style"`
	Prefix       string `json:"prefix"`
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
		next, err := c.updatedConfig(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.Save(next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		restart, err := c.applyReload(r.Context(), next)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_required": restart, "config": c.publicConfig()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *Core) runtimeStatus(w http.ResponseWriter, _ *http.Request) {
	c.runtimeMu.RLock()
	status := map[string]any{
		"database": map[string]any{
			"driver": c.cfg.Database.Driver,
		},
		"queue": map[string]any{
			"type":    queueType(c.cfg),
			"enabled": c.queue != nil,
			"stream":  c.cfg.Redis.Stream,
		},
		"storage": map[string]any{
			"type": c.cfg.Storage.Type,
		},
		"semantic_search": map[string]any{
			"enabled":  c.cfg.Search.Semantic.Enabled,
			"provider": c.cfg.Search.Semantic.Provider,
			"model":    c.cfg.Search.Semantic.Model,
			"ready":    c.semanticReady,
			"error":    c.semanticError,
		},
		"reload": map[string]any{
			"enabled":          c.cfg.Reload.Enabled,
			"restart_required": c.restartRequired,
			"last_reload_at":   c.lastReloadAt,
		},
	}
	c.runtimeMu.RUnlock()
	writeJSON(w, http.StatusOK, status)
}

func queueType(cfg *config.Config) string {
	if cfg.Redis.Enabled {
		return "redis_stream"
	}
	return "database_polling"
}

func (c *Core) reloadConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	restart, err := c.reloadFromDisk(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_required": restart, "config": c.publicConfig()})
}

func (c *Core) publicConfig() publicConfig {
	return publicConfig{
		App:      publicAppConfig{AdminTokenSet: c.cfg.App.AdminToken != ""},
		Server:   c.cfg.Server,
		NapCat:   publicNapCatConfig{Endpoint: c.cfg.NapCat.Endpoint, TokenSet: c.cfg.NapCat.Token != "", TimeoutSeconds: c.cfg.NapCat.TimeoutSeconds, MaxConcurrentRequests: c.cfg.NapCat.MaxConcurrentRequests, RetryMaxAttempts: c.cfg.NapCat.RetryMaxAttempts},
		Bot:      c.cfg.Bot,
		Website:  c.cfg.Website,
		Database: c.cfg.Database,
		Redis:    c.cfg.Redis,
		Storage: publicStorageConfig{
			Type:      c.cfg.Storage.Type,
			LocalRoot: c.cfg.Storage.LocalRoot,
			S3: publicS3Config{
				Endpoint:     c.cfg.Storage.S3.Endpoint,
				Bucket:       c.cfg.Storage.S3.Bucket,
				Region:       c.cfg.Storage.S3.Region,
				AccessKey:    c.cfg.Storage.S3.AccessKey,
				SecretKeySet: c.cfg.Storage.S3.SecretKey != "",
				UsePathStyle: c.cfg.Storage.S3.UsePathStyle,
				Prefix:       c.cfg.Storage.S3.Prefix,
			},
		},
		Worker:    c.cfg.Worker,
		RateLimit: c.cfg.RateLimit,
		Search:    c.cfg.Search,
		Reload:    c.cfg.Reload,
		Paths:     c.cfg.Paths,
	}
}

func (c *Core) reloadFromDisk(ctx context.Context) (bool, error) {
	next, err := config.Load(c.cfg.Paths.Config)
	if err != nil {
		return false, err
	}
	return c.applyReload(ctx, next)
}

func (c *Core) applyReload(ctx context.Context, next *config.Config) (bool, error) {
	c.runtimeMu.Lock()
	current := c.cfg
	restart := immutableChanged(current, next)
	c.runtimeMu.Unlock()

	timeout := time.Duration(next.NapCat.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	nc := napcat.New(next.NapCat.Endpoint, next.NapCat.Token, timeout, next.NapCat.MaxConcurrentRequests)
	maxBytes := next.Website.MaxFileSizeMB * 1024 * 1024
	dl := downloader.New(next.Website.UserAgent, maxBytes, next.Worker.BufferSizeKB*1024)

	var st storage.Storage
	if !restart || strings.EqualFold(current.Storage.Type, next.Storage.Type) {
		var err error
		st, err = openStorage(ctx, next)
		if err != nil {
			return restart, err
		}
	} else {
		st = c.storage
	}

	c.runtimeMu.Lock()
	*c.cfg = *next
	c.napcat = nc
	c.storage = st
	c.restartRequired = restart
	c.lastReloadAt = time.Now()
	c.configureSemantic(c.cfg)
	c.runtimeMu.Unlock()

	c.gateway.Reload(c.cfg, nc, st)
	c.workers.Reload(c.cfg, nc, dl, st)
	if c.cfg.Search.Semantic.Enabled {
		go c.refreshSemanticIndex(ctx)
	}
	return restart, nil
}

func immutableChanged(a, b *config.Config) bool {
	if a.Server.Listen != b.Server.Listen {
		return true
	}
	if !strings.EqualFold(a.Database.Driver, b.Database.Driver) || a.Database.Path != b.Database.Path || a.Database.DSN != b.Database.DSN {
		return true
	}
	if a.Redis.Enabled != b.Redis.Enabled || a.Redis.Addr != b.Redis.Addr || a.Redis.DB != b.Redis.DB ||
		a.Redis.Stream != b.Redis.Stream || a.Redis.ConsumerGroup != b.Redis.ConsumerGroup {
		return true
	}
	return !strings.EqualFold(a.Storage.Type, b.Storage.Type)
}

func (c *Core) watchConfig(ctx context.Context) {
	path := c.cfg.Paths.Config
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	lastMod := info.ModTime()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil || !info.ModTime().After(lastMod) {
				continue
			}
			lastMod = info.ModTime()
			debounce := time.Duration(c.cfg.Reload.DebounceMS) * time.Millisecond
			if debounce <= 0 {
				debounce = 500 * time.Millisecond
			}
			timer := time.NewTimer(debounce)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			restart, err := c.reloadFromDisk(ctx)
			if err != nil {
				log.Printf("config hot reload failed: %v", err)
				continue
			}
			log.Printf("config hot reloaded restart_required=%v", restart)
		}
	}
}

func (c *Core) updatedConfig(req publicConfig) (*config.Config, error) {
	if req.Server.Listen == "" {
		return nil, errors.New("server.listen is required")
	}
	if req.NapCat.Endpoint == "" {
		return nil, errors.New("napcat.endpoint is required")
	}
	if req.Website.MaxFileSizeMB <= 0 {
		return nil, errors.New("website.max_file_size_mb must be positive")
	}
	if req.Worker.MaxActiveTasks <= 0 || req.Worker.BufferSizeKB <= 0 || req.Worker.MaxRetries < 0 {
		return nil, errors.New("worker settings are invalid")
	}
	if req.Search.HighConfidence <= 0 || req.Search.HighConfidence > 1 {
		return nil, errors.New("search.high_confidence must be between 0 and 1")
	}
	if req.Search.MaxBatchFiles <= 0 || req.Search.MaxBatchSizeMB <= 0 {
		return nil, errors.New("search batch limits must be positive")
	}
	c.runtimeMu.RLock()
	next := *c.cfg
	c.runtimeMu.RUnlock()
	next.Server = req.Server
	next.NapCat.Endpoint = req.NapCat.Endpoint
	next.NapCat.TimeoutSeconds = req.NapCat.TimeoutSeconds
	next.NapCat.MaxConcurrentRequests = req.NapCat.MaxConcurrentRequests
	next.NapCat.RetryMaxAttempts = req.NapCat.RetryMaxAttempts
	if req.NapCat.Token != "" {
		next.NapCat.Token = req.NapCat.Token
	}
	if req.App.AdminToken != "" {
		next.App.AdminToken = req.App.AdminToken
	}
	next.Bot = req.Bot
	next.Website = req.Website
	next.Database = req.Database
	next.Redis = req.Redis
	next.Storage.Type = req.Storage.Type
	next.Storage.LocalRoot = req.Storage.LocalRoot
	next.Storage.S3.Endpoint = req.Storage.S3.Endpoint
	next.Storage.S3.Bucket = req.Storage.S3.Bucket
	next.Storage.S3.Region = req.Storage.S3.Region
	next.Storage.S3.AccessKey = req.Storage.S3.AccessKey
	next.Storage.S3.UsePathStyle = req.Storage.S3.UsePathStyle
	next.Storage.S3.Prefix = req.Storage.S3.Prefix
	if req.Storage.S3.SecretKey != "" {
		next.Storage.S3.SecretKey = req.Storage.S3.SecretKey
	}
	next.Worker = req.Worker
	next.RateLimit = req.RateLimit
	next.Search = req.Search
	next.Reload = req.Reload
	if next.Storage.LocalRoot == "" {
		next.Storage.LocalRoot = next.Paths.FilesDir
	}
	return &next, nil
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
	c.runtimeMu.RLock()
	semanticClient := c.semantic
	vectorIndex := c.vectorIndex
	semanticEnabled := c.cfg.Search.Semantic.Enabled
	c.runtimeMu.RUnlock()
	if semanticEnabled && semanticClient != nil && vectorIndex != nil {
		if vec, err := semanticClient.Embed(r.Context(), q); err == nil {
			semanticResults := vectorIndex.Search(vec, groupID, r.URL.Query().Get("ext"), limit)
			results = search.MergeResults(results, semanticResults, limit)
		} else {
			c.setSemanticState(vectorIndex, false, err.Error())
			log.Printf("semantic query fallback to text search: %v", err)
		}
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
