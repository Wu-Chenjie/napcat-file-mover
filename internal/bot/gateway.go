package bot

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/napcat"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/search"
	"napcat-file-mover/internal/security"
	"napcat-file-mover/internal/worker"
)

type Gateway struct {
	cfg    *config.Config
	repo   *repository.SQLite
	napcat *napcat.Client
}

func NewGateway(cfg *config.Config, repo *repository.SQLite, nc *napcat.Client) *Gateway {
	return &Gateway{cfg: cfg, repo: repo, napcat: nc}
}

func (g *Gateway) HandleEvent(ctx context.Context, ev napcat.OneBotEvent, ip string) {
	if ev.PostType != "message" || ev.MessageType != "group" {
		return
	}
	cmd, ok := Parse(ev.RawMessage, ev.UserID, ev.GroupID)
	if !ok {
		return
	}
	if !security.ContainsInt64(g.cfg.Bot.Admins, ev.UserID) {
		g.reply(ctx, ev.GroupID, "没有权限执行该命令")
		g.repo.Audit(ctx, ev.UserID, ev.GroupID, cmd.Raw, "command", "denied", ip)
		return
	}
	if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, ev.GroupID) {
		g.reply(ctx, ev.GroupID, "当前群不在白名单")
		g.repo.Audit(ctx, ev.UserID, ev.GroupID, cmd.Raw, "command", "group_denied", ip)
		return
	}
	var msg string
	var err error
	switch cmd.Name {
	case "搬运网页":
		msg, err = g.handleWeb(ctx, cmd)
	case "搬运群文件", "同步群文件":
		msg, err = g.handleGroupFiles(ctx, cmd)
	case "搜索文件":
		msg, err = g.handleSearch(ctx, cmd)
	case "搬运主题":
		msg, err = g.handleTopicTransfer(ctx, cmd)
	case "任务状态":
		msg, err = g.handleStatus(ctx, cmd)
	case "重试任务":
		msg, err = g.handleRetry(ctx, cmd)
	default:
		msg = "未知命令"
	}
	if err != nil {
		msg = err.Error()
		g.repo.Audit(ctx, ev.UserID, ev.GroupID, cmd.Raw, "command", "failed: "+err.Error(), ip)
	} else {
		g.repo.Audit(ctx, ev.UserID, ev.GroupID, cmd.Raw, "command", "ok", ip)
	}
	g.reply(ctx, ev.GroupID, msg)
}

func (g *Gateway) handleGroupFiles(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 2 {
		return "", fmt.Errorf("用法: /搬运群文件 <源群号> <目标群号|storage> [文件夹ID]")
	}
	srcGroup, err := strconv.ParseInt(cmd.Args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("源群号无效")
	}
	if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, srcGroup) {
		return "", fmt.Errorf("源群不在白名单")
	}
	target := cmd.Args[1]
	folderID := ""
	if len(cmd.Args) > 2 {
		folderID = cmd.Args[2]
	}
	var files []napcat.QQFile
	if folderID == "" {
		files, err = g.napcat.GetGroupRootFiles(ctx, srcGroup)
	} else {
		files, err = g.napcat.GetGroupFilesByFolder(ctx, srcGroup, folderID)
	}
	if err != nil {
		return "", err
	}
	targetGroup := int64(0)
	targetType := "storage"
	taskType := repository.TaskQQToStorage
	if target != "storage" {
		targetGroup, err = strconv.ParseInt(target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("目标群号无效")
		}
		if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, targetGroup) {
			return "", fmt.Errorf("目标群不在白名单")
		}
		targetType = "qq"
		taskType = repository.TaskQQToQQ
	}
	created := 0
	for _, f := range files {
		name := security.SanitizeFilename(f.FileName)
		idx := search.BuildIndexedText(name, f.FolderName, folderID)
		_ = g.repo.UpsertCatalog(ctx, repository.FileCatalog{
			GroupID: srcGroup, FolderID: folderID, FolderPath: f.FolderName, FileID: f.FileID, BusID: f.BusID,
			FileName: name, Ext: strings.ToLower(filepath.Ext(name)), FileSize: f.FileSize,
			NormalizedText: idx.Normalized, Pinyin: idx.Pinyin, Initials: idx.Initials, NGrams: idx.NGrams,
		})
		t := &repository.Task{
			TaskType: taskType, SourceType: "qq", SourceGroupID: srcGroup, SourceFileID: f.FileID, SourceBusID: f.BusID, SourceFolderID: folderID,
			TargetType: targetType, TargetGroupID: targetGroup, FileName: name, FileSize: f.FileSize,
			IdempotencyKey: worker.Idempotency("qq", strconv.FormatInt(srcGroup, 10), f.FileID, strconv.Itoa(int(f.BusID)), target),
			MaxRetries:     g.cfg.Worker.MaxRetries, CreatedBy: cmd.UserID,
		}
		id, err := g.repo.CreateTask(ctx, t)
		if err != nil {
			return "", err
		}
		if id != 0 {
			created++
		}
	}
	return fmt.Sprintf("扫描到 %d 个文件，已创建 %d 个搬运任务", len(files), created), nil
}

func (g *Gateway) handleWeb(ctx context.Context, cmd Command) (string, error) {
	task, err := WebTask(cmd, g.cfg.Website.AllowedHosts, g.cfg.Worker.MaxRetries)
	if err != nil {
		return "", err
	}
	id, err := g.repo.CreateTask(ctx, task)
	if err != nil {
		return "", err
	}
	if id == 0 {
		return "任务已存在，跳过去重创建", nil
	}
	return fmt.Sprintf("已创建任务 #%d", id), nil
}

func (g *Gateway) handleSearch(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 1 {
		return "", fmt.Errorf("用法: /搜索文件 <主题>")
	}
	q := strings.Join(cmd.Args, " ")
	indexed := search.BuildIndexedText(q)
	results, err := g.repo.SearchFiles(ctx, indexed.Normalized+" "+indexed.Pinyin+" "+indexed.Initials+" "+indexed.NGrams, 0, "", 10)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "没有找到匹配文件", nil
	}
	var b strings.Builder
	b.WriteString("匹配文件:\n")
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s (%d bytes, score %.2f)\n", i+1, r.FileName, r.FileSize, r.Score)
	}
	return strings.TrimSpace(b.String()), nil
}

func (g *Gateway) handleTopicTransfer(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 2 {
		return "", fmt.Errorf("用法: /搬运主题 <主题> <目标群号|storage> [源群号]")
	}
	target := cmd.Args[len(cmd.Args)-1]
	topic := strings.Join(cmd.Args[:len(cmd.Args)-1], " ")
	targetGroup := int64(0)
	targetType := "storage"
	if target != "storage" {
		v, err := strconv.ParseInt(target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("目标群号无效")
		}
		targetGroup = v
		targetType = "qq"
	}
	indexed := search.BuildIndexedText(topic)
	results, err := g.repo.SearchFiles(ctx, indexed.Normalized+" "+indexed.Pinyin+" "+indexed.Initials+" "+indexed.NGrams, 0, "", g.cfg.Search.MaxBatchFiles)
	if err != nil {
		return "", err
	}
	created := 0
	total := int64(0)
	for _, r := range results {
		if r.Score < g.cfg.Search.HighConfidence {
			continue
		}
		total += r.FileSize
		if g.cfg.Search.MaxBatchSizeMB > 0 && total > g.cfg.Search.MaxBatchSizeMB*1024*1024 {
			break
		}
		t := &repository.Task{
			TaskType: repository.TaskQQToStorage, SourceType: "qq", SourceGroupID: r.GroupID, SourceFileID: r.FileID, SourceBusID: r.BusID,
			TargetType: targetType, TargetGroupID: targetGroup, FileName: r.FileName, FileSize: r.FileSize,
			IdempotencyKey: worker.Idempotency("topic", topic, strconv.FormatInt(r.GroupID, 10), r.FileID, target), MaxRetries: g.cfg.Worker.MaxRetries, CreatedBy: cmd.UserID,
		}
		if targetType == "qq" {
			t.TaskType = repository.TaskQQToQQ
		}
		id, err := g.repo.CreateTask(ctx, t)
		if err != nil {
			return "", err
		}
		if id != 0 {
			created++
		}
	}
	if created == 0 {
		return "没有高置信度匹配文件，先使用 /搜索文件 查看候选", nil
	}
	return fmt.Sprintf("已创建 %d 个主题搬运任务", created), nil
}

func (g *Gateway) handleStatus(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) != 1 {
		return "", fmt.Errorf("用法: /任务状态 <任务ID>")
	}
	id, err := strconv.ParseInt(cmd.Args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("任务ID无效")
	}
	t, err := g.repo.GetTask(ctx, id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("任务 #%d: %s %s %s", t.ID, t.Status, t.FileName, t.LastError), nil
}

func (g *Gateway) handleRetry(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) != 1 {
		return "", fmt.Errorf("用法: /重试任务 <任务ID>")
	}
	id, err := strconv.ParseInt(cmd.Args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("任务ID无效")
	}
	return "已提交重试", g.repo.RetryTask(ctx, id)
}

func (g *Gateway) reply(ctx context.Context, groupID int64, msg string) {
	if msg == "" {
		return
	}
	_ = g.napcat.SendGroupMsg(ctx, groupID, msg)
}
