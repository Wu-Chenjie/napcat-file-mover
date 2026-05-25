package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/napcat"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/search"
)

func TestHandleMoveScansCurrentGroupWhenCatalogMisses(t *testing.T) {
	repo, err := repository.OpenSQLite(filepath.Join(t.TempDir(), "mover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.TrimPrefix(r.URL.Path, "/")
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "get_group_root_files":
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[{"file_id":"root-a","busid":1,"file_name":"不相关.txt","file_size":10}],"folders":[{"folder_id":"folder-a","folder_name":"资料"}]}}`))
		case "get_group_files_by_folder":
			var req struct {
				FolderID string `json:"folder_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.FolderID != "folder-a" {
				t.Fatalf("unexpected folder id: %s", req.FolderID)
			}
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[{"file_id":"file-a","busid":2,"file_name":"线代期末讲义.pdf","file_size":2048}],"folders":[]}}`))
		default:
			t.Fatalf("unexpected action: %s", action)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Bot.AllowedGroups = []int64{10001}
	cfg.Worker.MaxRetries = 3
	cfg.Search.MaxBatchFiles = 10
	cfg.Search.MaxBatchSizeMB = 1024
	nc := napcat.New(server.URL, "", 5*time.Second, 2)
	gateway := NewGateway(cfg, repo, nc)

	msg, err := gateway.handleMove(context.Background(), Command{
		Name:    "搬运",
		Args:    []string{"线代", "storage"},
		UserID:  42,
		GroupID: 10001,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "已创建 1 个搬运任务") {
		t.Fatalf("unexpected message: %s", msg)
	}
	tasks, err := repo.ListTasks(context.Background(), repository.TaskFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.SourceGroupID != 10001 || task.SourceFolderID != "folder-a" || task.SourceFileID != "file-a" {
		t.Fatalf("unexpected task source: %#v", task)
	}
	if task.FileName != "线代期末讲义.pdf" || task.TaskType != repository.TaskQQToStorage {
		t.Fatalf("unexpected task: %#v", task)
	}
}

func TestHandleHelpAllowedForNonAdmin(t *testing.T) {
	var message string
	var groupID int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.TrimPrefix(r.URL.Path, "/")
		if action != "send_group_msg" {
			t.Fatalf("unexpected action: %s", action)
		}
		var req struct {
			GroupID int64  `json:"group_id"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		groupID = req.GroupID
		message = req.Message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{}}`))
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Bot.AllowedGroups = []int64{10001}
	nc := napcat.New(server.URL, "", 5*time.Second, 2)
	gateway := NewGateway(cfg, nil, nc)

	gateway.HandleEvent(context.Background(), napcat.OneBotEvent{
		PostType:    "message",
		MessageType: "group",
		GroupID:     10001,
		UserID:      7,
		RawMessage:  "/help",
	}, "")

	if groupID != 10001 {
		t.Fatalf("unexpected group id: %d", groupID)
	}
	if !strings.Contains(message, "/help") || !strings.Contains(message, "/\u642c\u8fd0") {
		t.Fatalf("unexpected help message: %q", message)
	}
}

func TestSearchCatalogFindsChineseFilename(t *testing.T) {
	repo, err := repository.OpenSQLite(filepath.Join(t.TempDir(), "mover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	fileName := "附件2：未来技术拔尖班对接培养方案(1).docx"
	idx := search.BuildIndexedText(fileName)
	if err := repo.UpsertCatalog(context.Background(), repository.FileCatalog{
		GroupID: 10001, FileID: "file-a", BusID: 1, FileName: fileName,
		Ext: ".docx", FileSize: 1024,
		NormalizedText: idx.Normalized, Pinyin: idx.Pinyin, Initials: idx.Initials, NGrams: idx.NGrams,
	}); err != nil {
		t.Fatal(err)
	}

	gateway := NewGateway(&config.Config{}, repo, nil)
	results, err := gateway.searchCatalog(context.Background(), "未来技术", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].FileName != fileName {
		t.Fatalf("unexpected result: %#v", results[0])
	}
}

func TestHandleMoveURLDefaultsToCurrentGroupUpload(t *testing.T) {
	repo, err := repository.OpenSQLite(filepath.Join(t.TempDir(), "mover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	cfg := &config.Config{}
	cfg.Bot.AllowedGroups = []int64{10001}
	cfg.Website.AllowedHosts = []string{"github.com"}
	cfg.Worker.MaxRetries = 3
	gateway := NewGateway(cfg, repo, nil)

	msg, err := gateway.handleMove(context.Background(), Command{
		Name:    "搬运",
		Args:    []string{"https://github.com/HITLittleZheng/HITCS/blob/main/README.md"},
		UserID:  42,
		GroupID: 10001,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "创建 1 个搬运任务") {
		t.Fatalf("unexpected message: %s", msg)
	}
	tasks, err := repo.ListTasks(context.Background(), repository.TaskFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.TaskType != repository.TaskWebToQQ || task.TargetType != "qq" || task.TargetGroupID != 10001 {
		t.Fatalf("expected web_to_qq current-group task, got %#v", task)
	}
}
