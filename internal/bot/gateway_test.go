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
	"napcat-file-mover/internal/websource"
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

func TestHandleSearchScansAllowedGroupsWhenCatalogMisses(t *testing.T) {
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
			var req struct {
				GroupID int64 `json:"group_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.GroupID == 20002 {
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[{"file_id":"remote-file","busid":2,"file_name":"cross-group-notes.pdf","file_size":2048}],"folders":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[],"folders":[]}}`))
		default:
			t.Fatalf("unexpected action: %s", action)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Bot.AllowedGroups = []int64{10001, 20002}
	cfg.Search.MaxBatchFiles = 10
	cfg.Search.MaxBatchSizeMB = 1024
	nc := napcat.New(server.URL, "", 5*time.Second, 4)
	gateway := NewGateway(cfg, repo, nc)

	msg, err := gateway.handleSearch(context.Background(), Command{
		Name:    "搜索文件",
		Args:    []string{"cross-group"},
		UserID:  42,
		GroupID: 10001,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "cross-group-notes.pdf") {
		t.Fatalf("expected cross-group file in search result, got: %s", msg)
	}
}

func TestHandleMoveScansAllowedGroupsWhenCatalogMisses(t *testing.T) {
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
			var req struct {
				GroupID int64 `json:"group_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.GroupID == 20002 {
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[{"file_id":"remote-file","busid":2,"file_name":"cross-group-notes.pdf","file_size":2048}],"folders":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[],"folders":[]}}`))
		default:
			t.Fatalf("unexpected action: %s", action)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Bot.AllowedGroups = []int64{10001, 20002}
	cfg.Worker.MaxRetries = 3
	cfg.Search.MaxBatchFiles = 10
	cfg.Search.MaxBatchSizeMB = 1024
	nc := napcat.New(server.URL, "", 5*time.Second, 4)
	gateway := NewGateway(cfg, repo, nc)

	msg, err := gateway.handleMove(context.Background(), Command{
		Name:    "搬运",
		Args:    []string{"cross-group"},
		UserID:  42,
		GroupID: 10001,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1") {
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
	if task.SourceGroupID != 20002 || task.TargetGroupID != 10001 || task.TaskType != repository.TaskQQToQQ {
		t.Fatalf("expected cross-group qq_to_qq task, got %#v", task)
	}
}

func TestHandleMoveCurrentGroupMatchReturnsFileNameOnly(t *testing.T) {
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
			var req struct {
				GroupID int64 `json:"group_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.GroupID == 10001 {
				_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[{"file_id":"local-file","busid":2,"file_name":"local-notes.pdf","file_size":2048}],"folders":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"files":[],"folders":[]}}`))
		default:
			t.Fatalf("unexpected action: %s", action)
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Bot.AllowedGroups = []int64{10001, 20002}
	cfg.Worker.MaxRetries = 3
	cfg.Search.MaxBatchFiles = 10
	cfg.Search.MaxBatchSizeMB = 1024
	nc := napcat.New(server.URL, "", 5*time.Second, 4)
	gateway := NewGateway(cfg, repo, nc)

	msg, err := gateway.handleMove(context.Background(), Command{
		Name:    "搬运",
		Args:    []string{"local-notes"},
		UserID:  42,
		GroupID: 10001,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "local-notes.pdf") {
		t.Fatalf("expected local filename in message, got: %s", msg)
	}
	tasks, err := repo.ListTasks(context.Background(), repository.TaskFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected no task for current-group match, got %#v", tasks)
	}
}

func TestHandleHelpAllowedForNonAdmin(t *testing.T) {
	var message string
	var groupID int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.TrimPrefix(r.URL.Path, "/")
		w.Header().Set("Content-Type", "application/json")
		if action == "get_login_info" {
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"user_id":10000,"nickname":"TestBot"}}`))
			return
		}
		if action != "send_group_forward_msg" {
			t.Fatalf("unexpected action: %s", action)
		}
		var req struct {
			GroupID  int64 `json:"group_id"`
			Messages []struct {
				Data struct {
					Content []struct {
						Type string `json:"type"`
						Data struct {
							Text string `json:"text"`
						} `json:"data"`
					} `json:"content"`
				} `json:"data"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		groupID = req.GroupID
		if len(req.Messages) > 0 && len(req.Messages[0].Data.Content) > 0 {
			message = req.Messages[0].Data.Content[0].Data.Text
		}
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

func TestHandleWebSearchIncludesDownloadLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"content":[{"name":"link-target.pdf","is_dir":false,"size":10,"modified":"2026-01-02T03:04:05+08:00"}]}}`))
		case "/repos/HITLittleZheng/HITCS/git/trees/main":
			_, _ = w.Write([]byte(`{"tree":[]}`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	gateway := NewGateway(&config.Config{}, nil, nil)
	gateway.resolver = websource.NewResolver(websource.Options{
		HTTPClient:            server.Client(),
		FireworksListEndpoint: server.URL + "/api/fs/list",
		FireworksDownloadBase: "https://example.test/files",
		GitHubAPIBase:         server.URL,
	})

	msg, err := gateway.handleWebSearch(context.Background(), Command{
		Name: "搜索网页",
		Args: []string{"link-target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "link-target.pdf") || !strings.Contains(msg, "https://example.test/files/link-target.pdf") {
		t.Fatalf("expected filename and download link, got: %s", msg)
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
