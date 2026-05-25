package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/downloader"
	"napcat-file-mover/internal/napcat"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/storage"
)

func TestWebToQQSendsMergedForwardWithFile(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from forwarded file"))
	}))
	defer source.Close()

	var forwardReq struct {
		GroupID  int64 `json:"group_id"`
		Messages []struct {
			Type string `json:"type"`
			Data struct {
				Content []struct {
					Type string         `json:"type"`
					Data map[string]any `json:"data"`
				} `json:"content"`
			} `json:"data"`
		} `json:"messages"`
	}
	napcatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := filepath.Base(r.URL.Path)
		if action != "send_group_forward_msg" {
			t.Fatalf("expected send_group_forward_msg, got %s", action)
		}
		if err := json.NewDecoder(r.Body).Decode(&forwardReq); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":1,"forward_id":"abc"}}`))
	}))
	defer napcatServer.Close()

	temp := t.TempDir()
	cfg := &config.Config{
		Paths:     config.Paths{CacheDir: filepath.Join(temp, "cache"), FilesDir: filepath.Join(temp, "files")},
		NapCat:    config.NapCatConfig{MaxConcurrentRequests: 1},
		RateLimit: config.RateLimitConfig{GlobalDownloads: 1},
		Website:   config.WebsiteConfig{UserAgent: "test", MaxFileSizeMB: 10},
		Worker:    config.WorkerConfig{BufferSizeKB: 32},
	}
	pool := New(
		cfg,
		nil,
		napcat.New(napcatServer.URL, "", 5*time.Second, 1),
		downloader.New("test", 10*1024*1024, 32*1024),
		storage.NewLocal(cfg.Paths.FilesDir),
	)

	task := &repository.Task{
		ID:            7,
		TaskType:      repository.TaskWebToQQ,
		SourceType:    "web",
		SourceURL:     source.URL + "/README.md",
		TargetType:    "qq",
		TargetGroupID: 10001,
		FileName:      "README.md",
	}
	if err := pool.handle(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	if forwardReq.GroupID != 10001 {
		t.Fatalf("unexpected group id: %d", forwardReq.GroupID)
	}
	if len(forwardReq.Messages) != 2 {
		t.Fatalf("expected 2 forward nodes, got %d", len(forwardReq.Messages))
	}
	if forwardReq.Messages[0].Type != "node" || forwardReq.Messages[1].Type != "node" {
		t.Fatalf("unexpected node types: %#v", forwardReq.Messages)
	}
	if len(forwardReq.Messages[0].Data.Content) != 1 || forwardReq.Messages[0].Data.Content[0].Type != "text" {
		t.Fatalf("first node should be text metadata: %#v", forwardReq.Messages[0])
	}
	if len(forwardReq.Messages[1].Data.Content) != 1 || forwardReq.Messages[1].Data.Content[0].Type != "file" {
		t.Fatalf("second node should be file: %#v", forwardReq.Messages[1])
	}
	if forwardReq.Messages[1].Data.Content[0].Data["name"] != "README.md" {
		t.Fatalf("unexpected file node: %#v", forwardReq.Messages[1].Data.Content[0])
	}
}
