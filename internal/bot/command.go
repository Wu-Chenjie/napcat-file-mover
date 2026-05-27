package bot

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/security"
	"napcat-file-mover/internal/worker"
)

type Command struct {
	Name    string
	Args    []string
	Raw     string
	UserID  int64
	GroupID int64
}

func Parse(raw string, userID, groupID int64) (Command, bool) {
	raw = strings.TrimSpace(raw)
	prefix := ""
	switch {
	case strings.HasPrefix(raw, "/"):
		prefix = "/"
	case strings.HasPrefix(strings.ToLower(raw), "#napcat"):
		raw = strings.TrimSpace("#help" + strings.TrimPrefix(raw, raw[:len("#napcat")]))
		prefix = "#"
	case strings.HasPrefix(raw, "#"):
		prefix = "#"
	default:
		return Command{}, false
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return Command{}, false
	}
	return Command{Name: strings.TrimPrefix(parts[0], prefix), Args: parts[1:], Raw: raw, UserID: userID, GroupID: groupID}, true
}

func WebTask(cmd Command, allowedHosts []string, maxRetries int) (*repository.Task, error) {
	if len(cmd.Args) < 2 {
		return nil, fmt.Errorf("用法: /搬运网页 <URL> <目标群号|storage>")
	}
	rawURL := cmd.Args[0]
	if !security.IsAllowedHost(rawURL, allowedHosts) {
		return nil, fmt.Errorf("网站不在白名单")
	}
	target := cmd.Args[1]
	name := fileNameFromURL(rawURL)
	if target == "storage" {
		return &repository.Task{
			TaskType: repository.TaskWebToStorage, SourceType: "web", SourceURL: rawURL, TargetType: "storage",
			FileName: name, IdempotencyKey: worker.Idempotency("web", rawURL, "storage"), MaxRetries: maxRetries, CreatedBy: cmd.UserID,
		}, nil
	}
	groupID, err := strconv.ParseInt(target, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("目标群号无效")
	}
	return &repository.Task{
		TaskType: repository.TaskWebToQQ, SourceType: "web", SourceURL: rawURL, TargetType: "qq", TargetGroupID: groupID,
		FileName: name, IdempotencyKey: worker.Idempotency("web", rawURL, "qq", target), MaxRetries: maxRetries, CreatedBy: cmd.UserID,
	}, nil
}

func fileNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "download.bin"
	}
	name := filepath.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		return "download.bin"
	}
	return security.SanitizeFilename(name)
}
