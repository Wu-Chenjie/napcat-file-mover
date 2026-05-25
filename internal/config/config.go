package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
)

const AppName = "NapCatFileMover"

type Config struct {
	App       AppConfig       `yaml:"app"`
	Server    ServerConfig    `yaml:"server"`
	NapCat    NapCatConfig    `yaml:"napcat"`
	Bot       BotConfig       `yaml:"bot"`
	Website   WebsiteConfig   `yaml:"website"`
	Database  DatabaseConfig  `yaml:"database"`
	Storage   StorageConfig   `yaml:"storage"`
	Worker    WorkerConfig    `yaml:"worker"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Search    SearchConfig    `yaml:"search"`
	Paths     Paths           `yaml:"-"`
}

type AppConfig struct {
	AdminToken string `yaml:"admin_token"`
}

type ServerConfig struct {
	Listen string `yaml:"listen"`
}

type NapCatConfig struct {
	Endpoint              string        `yaml:"endpoint"`
	Token                 string        `yaml:"token"`
	WSURI                 string        `yaml:"ws_uri"`
	TimeoutSeconds        int           `yaml:"timeout_seconds"`
	MaxConcurrentRequests int           `yaml:"max_concurrent_requests"`
	RetryMaxAttempts      int           `yaml:"retry_max_attempts"`
	RetryBaseDelay        time.Duration `yaml:"-"`
}

type BotConfig struct {
	Admins        []int64 `yaml:"admins"`
	AllowedGroups []int64 `yaml:"allowed_groups"`
}

type WebsiteConfig struct {
	AllowedHosts  []string `yaml:"allowed_hosts"`
	MaxFileSizeMB int64    `yaml:"max_file_size_mb"`
	UserAgent     string   `yaml:"user_agent"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type StorageConfig struct {
	LocalRoot string `yaml:"local_root"`
}

type WorkerConfig struct {
	DownloadWorkers int `yaml:"download_workers"`
	UploadWorkers   int `yaml:"upload_workers"`
	MaxActiveTasks  int `yaml:"max_active_tasks"`
	BufferSizeKB    int `yaml:"buffer_size_kb"`
	MaxRetries      int `yaml:"max_retries"`
}

type RateLimitConfig struct {
	QQAPIRPS         int `yaml:"qq_api_rps"`
	GroupUploadRPM   int `yaml:"group_upload_rpm"`
	PerHostDownloads int `yaml:"per_host_downloads"`
	GlobalDownloads  int `yaml:"global_downloads"`
}

type SearchConfig struct {
	EmbeddingEndpoint string  `yaml:"embedding_endpoint"`
	HighConfidence    float64 `yaml:"high_confidence"`
	MaxBatchFiles     int     `yaml:"max_batch_files"`
	MaxBatchSizeMB    int64   `yaml:"max_batch_size_mb"`
}

type Paths struct {
	BaseDir   string
	Config    string
	Database  string
	CacheDir  string
	FilesDir  string
	LogDir    string
	StaticDir string
}

func Load(path string) (*Config, error) {
	paths, err := ResolvePaths(path)
	if err != nil {
		return nil, err
	}

	cfg := Default(paths)
	if path == "" {
		path = paths.Config
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		b, _ := yaml.Marshal(cfg)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, fmt.Errorf("write default config: %w", err)
		}
	} else if err == nil {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	cfg.Paths = paths
	applyPathDefaults(cfg)
	return cfg, ensureDirs(paths)
}

func Save(cfg *Config) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	applyPathDefaults(cfg)
	if err := ensureDirs(cfg.Paths); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Paths.Config), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(cfg.Paths.Config, b, 0o600)
}

func Default(paths Paths) *Config {
	return &Config{
		App:    AppConfig{AdminToken: "change-me"},
		Server: ServerConfig{Listen: "127.0.0.1:8088"},
		NapCat: NapCatConfig{
			Endpoint:              "http://127.0.0.1:3000",
			Token:                 "change-me",
			TimeoutSeconds:        30,
			MaxConcurrentRequests: 8,
			RetryMaxAttempts:      3,
		},
		Website: WebsiteConfig{
			MaxFileSizeMB: 2048,
			UserAgent:     "NapCatFileMover/0.1",
		},
		Database: DatabaseConfig{Path: paths.Database},
		Storage:  StorageConfig{LocalRoot: paths.FilesDir},
		Worker: WorkerConfig{
			DownloadWorkers: 16,
			UploadWorkers:   4,
			MaxActiveTasks:  32,
			BufferSizeKB:    512,
			MaxRetries:      5,
		},
		RateLimit: RateLimitConfig{
			QQAPIRPS:         2,
			GroupUploadRPM:   6,
			PerHostDownloads: 4,
			GlobalDownloads:  16,
		},
		Search: SearchConfig{
			HighConfidence: 0.72,
			MaxBatchFiles:  50,
			MaxBatchSizeMB: 2048,
		},
		Paths: paths,
	}
}

func ResolvePaths(configPath string) (Paths, error) {
	base, err := userDataDir()
	if err != nil {
		return Paths{}, err
	}
	paths := Paths{
		BaseDir:   base,
		Config:    filepath.Join(base, "config.yaml"),
		Database:  filepath.Join(base, "mover.db"),
		CacheDir:  filepath.Join(base, "cache"),
		FilesDir:  filepath.Join(base, "files"),
		LogDir:    filepath.Join(base, "logs"),
		StaticDir: filepath.Join(base, "static"),
	}
	if configPath != "" {
		paths.Config = configPath
	}
	return paths, nil
}

func userDataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("LOCALAPPDATA is not set")
		}
		return filepath.Join(base, AppName), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", AppName), nil
	default:
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, AppName), nil
	}
}

func ensureDirs(paths Paths) error {
	for _, dir := range []string{paths.BaseDir, paths.CacheDir, paths.FilesDir, paths.LogDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func applyPathDefaults(cfg *Config) {
	if cfg.Database.Path == "" {
		cfg.Database.Path = cfg.Paths.Database
	}
	if cfg.Storage.LocalRoot == "" {
		cfg.Storage.LocalRoot = cfg.Paths.FilesDir
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:8088"
	}
}
