package bot

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/napcat"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/search"
	"napcat-file-mover/internal/security"
	"napcat-file-mover/internal/websource"
	"napcat-file-mover/internal/worker"
)

var docExtensions = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true,
	".ppt": true, ".pptx": true,
	".xls": true, ".xlsx": true,
	".txt": true, ".md": true, ".csv": true,
	".zip": true, ".rar": true, ".7z": true, ".tar": true, ".gz": true,
}

func isDocFile(name string) bool {
	return docExtensions[strings.ToLower(filepath.Ext(name))]
}

type Gateway struct {
	cfg         *config.Config
	repo        *repository.SQLite
	napcat      *napcat.Client
	resolver    *websource.Resolver
	botUserID   string
	botNickname string
}

type qqCandidate struct {
	GroupID    int64
	FolderID   string
	FolderPath string
	FileID     string
	BusID      int32
	FileName   string
	FileSize   int64
}

func NewGateway(cfg *config.Config, repo *repository.SQLite, nc *napcat.Client) *Gateway {
	return &Gateway{
		cfg:      cfg,
		repo:     repo,
		napcat:   nc,
		resolver: websource.NewResolver(websource.Options{}),
	}
}

func (g *Gateway) HandleEvent(ctx context.Context, ev napcat.OneBotEvent, ip string) {
	log.Printf("[gateway] event: post_type=%s msg_type=%s group=%d user=%d raw=%s", ev.PostType, ev.MessageType, ev.GroupID, ev.UserID, ev.RawMessage)
	if ev.PostType != "message" || ev.MessageType != "group" {
		return
	}
	cmd, ok := Parse(ev.RawMessage, ev.UserID, ev.GroupID)
	if !ok {
		return
	}
	isAdmin := security.ContainsInt64(g.cfg.Bot.Admins, ev.UserID)
	if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, ev.GroupID) {
		g.reply(ctx, ev.GroupID, "当前群不在白名单")
		return
	}
	if !isAdmin && !isPublicCommand(cmd.Name) {
		g.reply(ctx, ev.GroupID, "没有权限执行该命令")
		return
	}
	var msg string
	var err error
	switch cmd.Name {
	case "help", "帮助":
		msg = helpMessage()
	case "搬运网页":
		msg, err = g.handleWeb(ctx, cmd)
	case "搬运群文件", "同步群文件":
		msg, err = g.handleGroupFiles(ctx, cmd)
	case "搜索文件":
		msg, err = g.handleSearch(ctx, cmd)
	case "搜索网页":
		msg, err = g.handleWebSearch(ctx, cmd)
	case "群文件":
		msg, err = g.handleListFiles(ctx, cmd)
	case "搬运主题":
		msg, err = g.handleTopicTransfer(ctx, cmd)
	case "搬运":
		msg, err = g.handleMove(ctx, cmd)
	case "任务状态":
		msg, err = g.handleStatus(ctx, cmd)
	case "重试任务":
		msg, err = g.handleRetry(ctx, cmd)
	default:
		msg = "未知命令，发送 /help 查看用法"
	}
	if err != nil {
		msg = err.Error()
	}
	log.Printf("[gateway] reply: cmd=%s msg=%s", cmd.Name, truncateStr(msg, 80))
	g.reply(ctx, ev.GroupID, msg)
}

func isPublicCommand(name string) bool {
	switch name {
	case "help", "帮助", "搬运", "搜索文件", "搜索网页", "群文件":
		return true
	default:
		return false
	}
}

func helpMessage() string {
	return "NapCatFileMover\n\n" +
		"/help - 帮助\n" +
		"/搜索文件 <关键词> - 搜索群文件\n" +
		"/搜索网页 <关键词> - 搜索网站文件\n" +
		"/搬运 <文件名|URL> [目标] - 搬运文件\n\n" +
		"管理员:\n" +
		"/搬运网页 <关键词|URL> [目标] - 搜索网站并搬运，默认当前群\n" +
		"/搬运群文件 /同步群文件\n" +
		"/搬运主题 /任务状态 /重试任务\n\n" +
		"支持: fireworks.jwyihao.top hoa.moe github.com"
}

func (g *Gateway) handleGroupFiles(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 2 {
		return "", fmt.Errorf("用法: /搬运群文件 <源群号> <目标群号|storage>")
	}
	srcGroup, err := strconv.ParseInt(cmd.Args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("源群号无效")
	}
	if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, srcGroup) {
		return "", fmt.Errorf("源群不在白名单")
	}
	target := cmd.Args[1]
	candidates, err := g.scanGroupFiles(ctx, srcGroup)
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
	skipped := 0
	for _, f := range candidates {
		name := security.SanitizeFilename(f.FileName)
		if !isDocFile(name) {
			skipped++
			continue
		}
		t := &repository.Task{
			TaskType: taskType, SourceType: "qq", SourceGroupID: srcGroup, SourceFileID: f.FileID, SourceBusID: f.BusID, SourceFolderID: f.FolderID,
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
	return fmt.Sprintf("共 %d 个文件，跳过 %d 个非资料，已创建 %d 个搬运任务", len(candidates), skipped, created), nil
}

func (g *Gateway) handleWeb(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 1 {
		return "", fmt.Errorf("用法: /搬运网页 <URL|关键词> [目标群号|storage]  目标默认当前群")
	}
	target := strconv.FormatInt(cmd.GroupID, 10)
	queryEnd := len(cmd.Args)
	last := cmd.Args[len(cmd.Args)-1]
	if last == "storage" || isNumeric(last) {
		target = last
		queryEnd = len(cmd.Args) - 1
	}
	query := strings.Join(cmd.Args[:queryEnd], " ")
	if query == "" {
		return "", fmt.Errorf("请输入要搬运的文件关键词或URL")
	}

	if !looksLikeURL(query) {
		return g.handleWebSearchTransfer(ctx, query, target, cmd.UserID)
	}

	if !security.IsAllowedHost(cmd.Args[0], g.cfg.Website.AllowedHosts) {
		return "", fmt.Errorf("网站不在白名单")
	}
	files, err := websource.NewResolver(websource.Options{}).Resolve(ctx, cmd.Args[0])
	if err == nil {
		cmd.Args = []string{cmd.Args[0], target}
		return g.createWebTasks(ctx, cmd, files)
	}
	if err != nil && err != websource.ErrUnsupported {
		return "", err
	}
	cmd.Args = []string{cmd.Args[0], target}
	task, err := WebTask(cmd, g.cfg.Website.AllowedHosts, g.cfg.Worker.MaxRetries)
	if err != nil {
		return "", err
	}
	id, err := g.repo.CreateTask(ctx, task)
	if err != nil {
		return "", err
	}
	if id == 0 {
		return "任务已存在，去重跳过", nil
	}
	return fmt.Sprintf("已创建搬运任务 #%d", id), nil
}

func (g *Gateway) handleWebSearchTransfer(ctx context.Context, query, target string, userID int64) (string, error) {
	files, err := g.resolver.SearchWeb(ctx, query, 20)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "网站未找到匹配文件", nil
	}
	targetGroup := int64(0)
	targetType := "storage"
	taskType := repository.TaskWebToStorage
	if target != "storage" {
		v, err := strconv.ParseInt(target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("目标群号无效")
		}
		if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, v) {
			return "", fmt.Errorf("目标群不在白名单")
		}
		targetGroup = v
		targetType = "qq"
		taskType = repository.TaskWebToQQ
	}
	created := 0
	for _, f := range files {
		if !isDocFile(f.Name) {
			continue
		}
		task := &repository.Task{
			TaskType:       taskType,
			SourceType:     "web",
			SourceURL:      f.URL,
			TargetType:     targetType,
			TargetGroupID:  targetGroup,
			FileName:       f.Name,
			FileSize:       f.Size,
			IdempotencyKey: worker.Idempotency("web-search", f.URL, target),
			MaxRetries:     g.cfg.Worker.MaxRetries,
			CreatedBy:      userID,
		}
		id, err := g.repo.CreateTask(ctx, task)
		if err != nil {
			return "", err
		}
		if id != 0 {
			created++
		}
	}
	if created == 0 {
		return "没有可搬运的资料文件", nil
	}
	return fmt.Sprintf("从网站搜索到 %d 个文件，已创建 %d 个搬运任务", len(files), created), nil
}

func (g *Gateway) createWebTasks(ctx context.Context, cmd Command, files []websource.File) (string, error) {
	if !security.IsAllowedHost(cmd.Args[0], g.cfg.Website.AllowedHosts) {
		return "", fmt.Errorf("网站不在白名单")
	}
	target := cmd.Args[1]
	targetGroup := int64(0)
	targetType := "storage"
	taskType := repository.TaskWebToStorage
	if target != "storage" {
		v, err := strconv.ParseInt(target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("目标群号无效")
		}
		if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, v) {
			return "", fmt.Errorf("目标群不在白名单")
		}
		targetGroup = v
		targetType = "qq"
		taskType = repository.TaskWebToQQ
	}
	created := 0
	skipped := 0
	total := int64(0)
	for _, f := range files {
		name := security.SanitizeFilename(f.Name)
		if !isDocFile(name) {
			skipped++
			continue
		}
		total += f.Size
		if g.cfg.Search.MaxBatchFiles > 0 && created >= g.cfg.Search.MaxBatchFiles {
			break
		}
		if g.cfg.Search.MaxBatchSizeMB > 0 && total > g.cfg.Search.MaxBatchSizeMB*1024*1024 {
			break
		}
		task := &repository.Task{
			TaskType: taskType, SourceType: "web", SourceURL: f.URL,
			TargetType: targetType, TargetGroupID: targetGroup,
			FileName: name, FileSize: f.Size,
			IdempotencyKey: worker.Idempotency("web-resolved", f.URL, target),
			MaxRetries:     g.cfg.Worker.MaxRetries, CreatedBy: cmd.UserID,
		}
		id, err := g.repo.CreateTask(ctx, task)
		if err != nil {
			return "", err
		}
		if id != 0 {
			created++
		}
	}
	if created == 0 {
		return fmt.Sprintf("没有可搬运的资料文件，跳过 %d 个非资料文件", skipped), nil
	}
	return fmt.Sprintf("已从网页解析 %d 个文件，跳过 %d 个非资料，创建 %d 个搬运任务", len(files), skipped, created), nil
}

func (g *Gateway) handleWebSearch(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 1 {
		return "", fmt.Errorf("用法: /搜索网页 <关键词>")
	}
	q := strings.Join(cmd.Args, " ")
	files, err := g.resolver.SearchWeb(ctx, q, 15)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "没有匹配的文件", nil
	}
	var b strings.Builder
	b.WriteString("网站匹配文件:\n")
	for i, f := range files {
		fmt.Fprintf(&b, "%d. %s (%s)\n", i+1, f.Name, formatSize(f.Size))
	}
	return strings.TrimSpace(b.String()), nil
}

func (g *Gateway) handleListFiles(ctx context.Context, cmd Command) (string, error) {
	candidates, err := g.scanGroupFiles(ctx, cmd.GroupID)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "当前群没有文件", nil
	}
	var total int64
	folders := map[string][]qqCandidate{}
	for _, f := range candidates {
		fp := f.FolderPath
		if fp == "" {
			fp = "/"
		}
		folders[fp] = append(folders[fp], f)
		total += f.FileSize
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个文件 (%s)\n\n", len(candidates), formatSize(total))
	count := 0
	for folder, files := range folders {
		if count >= 30 {
			break
		}
		fmt.Fprintf(&b, "[%s] (%d个)\n", folder, len(files))
		for _, f := range files {
			fmt.Fprintf(&b, "  - %s (%s)\n", f.FileName, formatSize(f.FileSize))
			count++
			if count >= 30 {
				break
			}
		}
		if count >= 30 {
			break
		}
	}
	if count < len(candidates) {
		fmt.Fprintf(&b, "\n... 还有 %d 个文件", len(candidates)-count)
	}
	return strings.TrimSpace(b.String()), nil
}

func (g *Gateway) handleSearch(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 1 {
		return "", fmt.Errorf("用法: /搜索文件 <关键词>")
	}
	q := strings.Join(cmd.Args, " ")
	results, err := g.searchCatalog(ctx, q, 15)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		if _, err := g.scanGroupFiles(ctx, cmd.GroupID); err != nil {
			return "", err
		}
		results, err = g.searchCatalog(ctx, q, 15)
		if err != nil {
			return "", err
		}
	}
	if len(results) == 0 {
		return "没有匹配文件", nil
	}
	var b strings.Builder
	b.WriteString("匹配文件:\n")
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s (%s, score %.2f)\n", i+1, r.FileName, formatSize(r.FileSize), r.Score)
	}
	return strings.TrimSpace(b.String()), nil
}

func (g *Gateway) handleTopicTransfer(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 2 {
		return "", fmt.Errorf("用法: /搬运主题 <主题> <目标群号|storage>")
	}
	target := cmd.Args[len(cmd.Args)-1]
	topic := strings.Join(cmd.Args[:len(cmd.Args)-1], " ")
	return g.moveByName(ctx, cmd, topic, target)
}

func (g *Gateway) handleMove(ctx context.Context, cmd Command) (string, error) {
	if len(cmd.Args) < 1 {
		return "", fmt.Errorf("用法: /搬运 <文件名|URL> [目标群号|storage]  目标默认当前群")
	}
	target := strconv.FormatInt(cmd.GroupID, 10)
	topicStart := 0
	topicEnd := len(cmd.Args)
	last := cmd.Args[len(cmd.Args)-1]
	if last == "storage" || isNumeric(last) {
		target = last
		topicEnd = len(cmd.Args) - 1
	}
	if topicEnd <= topicStart {
		return "", fmt.Errorf("请输入要搬运的文件名")
	}
	if looksLikeURL(cmd.Args[0]) {
		return g.handleWeb(ctx, Command{
			Name:    "搬运网页",
			Args:    []string{cmd.Args[0], target},
			Raw:     cmd.Raw,
			UserID:  cmd.UserID,
			GroupID: cmd.GroupID,
		})
	}
	topic := strings.Join(cmd.Args[topicStart:topicEnd], " ")
	return g.moveByName(ctx, cmd, topic, target)
}

func (g *Gateway) moveByName(ctx context.Context, cmd Command, query, target string) (string, error) {
	targetGroup := int64(0)
	targetType := "storage"
	if target != "storage" {
		v, err := strconv.ParseInt(target, 10, 64)
		if err != nil {
			return "", fmt.Errorf("目标群号无效")
		}
		if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, v) {
			return "", fmt.Errorf("目标群不在白名单")
		}
		targetGroup = v
		targetType = "qq"
	}
	results, err := g.searchCatalog(ctx, query, g.cfg.Search.MaxBatchFiles)
	if err != nil {
		return "", err
	}
	created := 0
	total := int64(0)
	for _, r := range results {
		total += r.FileSize
		if g.cfg.Search.MaxBatchSizeMB > 0 && total > g.cfg.Search.MaxBatchSizeMB*1024*1024 {
			break
		}
		nt := &repository.Task{
			TaskType: repository.TaskQQToStorage, SourceType: "qq", SourceGroupID: r.GroupID,
			SourceFileID: r.FileID, SourceBusID: r.BusID, SourceFolderID: r.FolderID,
			TargetType: targetType, TargetGroupID: targetGroup,
			FileName: r.FileName, FileSize: r.FileSize,
			IdempotencyKey: worker.Idempotency("move", query, strconv.FormatInt(r.GroupID, 10), r.FileID, target),
			MaxRetries:     g.cfg.Worker.MaxRetries, CreatedBy: cmd.UserID,
		}
		if targetType == "qq" {
			nt.TaskType = repository.TaskQQToQQ
		}
		id, err := g.repo.CreateTask(ctx, nt)
		if err != nil {
			return "", err
		}
		if id != 0 {
			created++
		}
	}
	if created == 0 {
		candidates, err := g.scanGroupFiles(ctx, cmd.GroupID)
		if err != nil {
			return "", err
		}
		for _, candidate := range candidates {
			if !g.matchesQuery(candidate, query) {
				continue
			}
			if !isDocFile(candidate.FileName) {
				continue
			}
			total += candidate.FileSize
			if g.cfg.Search.MaxBatchSizeMB > 0 && total > g.cfg.Search.MaxBatchSizeMB*1024*1024 {
				break
			}
			nt := &repository.Task{
				TaskType: repository.TaskQQToStorage, SourceType: "qq", SourceGroupID: candidate.GroupID,
				SourceFileID: candidate.FileID, SourceBusID: candidate.BusID, SourceFolderID: candidate.FolderID,
				TargetType: targetType, TargetGroupID: targetGroup,
				FileName: candidate.FileName, FileSize: candidate.FileSize,
				IdempotencyKey: worker.Idempotency("move-live", query, strconv.FormatInt(candidate.GroupID, 10), candidate.FileID, target),
				MaxRetries:     g.cfg.Worker.MaxRetries, CreatedBy: cmd.UserID,
			}
			if targetType == "qq" {
				nt.TaskType = repository.TaskQQToQQ
			}
			id, err := g.repo.CreateTask(ctx, nt)
			if err != nil {
				return "", err
			}
			if id != 0 {
				created++
			}
		}
	}
	if created == 0 {
		return "没有匹配资料文件", nil
	}
	return fmt.Sprintf("已创建 %d 个搬运任务", created), nil
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

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (g *Gateway) reply(ctx context.Context, groupID int64, msg string) {
	if msg == "" {
		return
	}
	if g.botUserID == "" {
		info, err := g.napcat.GetLoginInfo(ctx)
		if err != nil {
			log.Printf("[reply] get bot info: %v", err)
			g.botUserID = "0"
			g.botNickname = "Bot"
		} else {
			g.botUserID = strconv.FormatInt(info.UserID, 10)
			g.botNickname = info.Nickname
		}
	}
	nodes := []napcat.ForwardNode{{
		UserID:   g.botUserID,
		Nickname: g.botNickname,
		Content:  []napcat.MessageSegment{{Type: "text", Data: map[string]any{"text": msg}}},
	}}
	if err := g.napcat.SendGroupForwardMsg(ctx, groupID, nodes); err != nil {
		log.Printf("[reply] error: %v", err)
	}
}

func (g *Gateway) searchCatalog(ctx context.Context, query string, limit int) ([]repository.SearchResult, error) {
	indexed := search.BuildIndexedText(query)
	queries := []string{indexed.Normalized, indexed.Pinyin, indexed.Initials, indexed.NGrams}
	out := make([]repository.SearchResult, 0, limit)
	seen := map[string]bool{}
	for _, q := range queries {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		results, err := g.repo.SearchFiles(ctx, q, 0, "", limit)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			key := fmt.Sprintf("%d:%s:%d", result.GroupID, result.FileName, result.FileSize)
			if seen[key] || !isDocFile(result.FileName) {
				continue
			}
			seen[key] = true
			out = append(out, result)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func (g *Gateway) scanGroupFiles(ctx context.Context, groupID int64) ([]qqCandidate, error) {
	if !security.ContainsInt64(g.cfg.Bot.AllowedGroups, groupID) {
		return nil, fmt.Errorf("群不在白名单")
	}
	return g.scanGroupFolder(ctx, groupID, "", "")
}

func (g *Gateway) scanGroupFolder(ctx context.Context, groupID int64, folderID, folderPath string) ([]qqCandidate, error) {
	var entries napcat.QQFileList
	var err error
	if folderID == "" {
		entries, err = g.napcat.GetGroupRootEntries(ctx, groupID)
	} else {
		entries, err = g.napcat.GetGroupFolderEntries(ctx, groupID, folderID)
	}
	if err != nil {
		return nil, err
	}
	out := make([]qqCandidate, 0, len(entries.Files))
	for _, f := range entries.Files {
		name := security.SanitizeFilename(f.FileName)
		candidate := qqCandidate{
			GroupID: groupID, FolderID: folderID, FolderPath: folderPath,
			FileID: f.FileID, BusID: f.BusID, FileName: name, FileSize: f.FileSize,
		}
		out = append(out, candidate)
		g.indexCandidate(ctx, candidate)
	}
	for _, folder := range entries.Folders {
		name := security.SanitizeFilename(folder.FolderName)
		childPath := name
		if folderPath != "" {
			childPath = folderPath + "/" + name
		}
		children, err := g.scanGroupFolder(ctx, groupID, folder.FolderID, childPath)
		if err != nil {
			return nil, err
		}
		out = append(out, children...)
	}
	return out, nil
}

func (g *Gateway) indexCandidate(ctx context.Context, candidate qqCandidate) {
	idx := search.BuildIndexedText(candidate.FileName, candidate.FolderPath)
	_ = g.repo.UpsertCatalog(ctx, repository.FileCatalog{
		GroupID: candidate.GroupID, FolderID: candidate.FolderID, FolderPath: candidate.FolderPath,
		FileID: candidate.FileID, BusID: candidate.BusID,
		FileName: candidate.FileName, Ext: strings.ToLower(filepath.Ext(candidate.FileName)), FileSize: candidate.FileSize,
		NormalizedText: idx.Normalized, Pinyin: idx.Pinyin, Initials: idx.Initials, NGrams: idx.NGrams,
	})
}

func (g *Gateway) matchesQuery(candidate qqCandidate, query string) bool {
	needle := search.BuildIndexedText(query)
	haystack := search.BuildIndexedText(candidate.FileName, candidate.FolderPath)
	if needle.Normalized != "" && strings.Contains(haystack.Normalized, needle.Normalized) {
		return true
	}
	if needle.Pinyin != "" && strings.Contains(haystack.Pinyin, needle.Pinyin) {
		return true
	}
	if needle.Initials != "" && strings.Contains(haystack.Initials, needle.Initials) {
		return true
	}
	return needle.NGrams != "" && strings.Contains(haystack.NGrams, needle.NGrams)
}

func looksLikeURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func formatSize(size int64) string {
	if size <= 0 {
		return "-"
	}
	if size > 1024*1024*1024 {
		return fmt.Sprintf("%.2f GB", float64(size)/1024/1024/1024)
	}
	if size > 1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(size)/1024/1024)
	}
	return fmt.Sprintf("%.1f KB", float64(size)/1024)
}

func isNumeric(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}
