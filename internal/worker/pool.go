package worker

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/downloader"
	"napcat-file-mover/internal/napcat"
	"napcat-file-mover/internal/queue"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/security"
	"napcat-file-mover/internal/storage"
)

type Pool struct {
	cfg        *config.Config
	repo       repository.Store
	napcat     *napcat.Client
	downloader *downloader.HTTPDownloader
	storage    storage.Storage
	queue      *queue.RedisStream
	limiter    *SemaphoreLimiter
	cancel     context.CancelFunc
}

func New(cfg *config.Config, repo repository.Store, nc *napcat.Client, dl *downloader.HTTPDownloader, st storage.Storage) *Pool {
	lim := NewLimiter(4)
	lim.Add("global:download", cfg.RateLimit.GlobalDownloads)
	lim.Add("qq:api", cfg.NapCat.MaxConcurrentRequests)
	return &Pool{cfg: cfg, repo: repo, napcat: nc, downloader: dl, storage: st, limiter: lim}
}

func (p *Pool) WithQueue(q *queue.RedisStream) *Pool {
	p.queue = q
	return p
}

func (p *Pool) Reload(cfg *config.Config, nc *napcat.Client, dl *downloader.HTTPDownloader, st storage.Storage) {
	p.cfg = cfg
	p.napcat = nc
	p.downloader = dl
	p.storage = st
	lim := NewLimiter(4)
	lim.Add("global:download", cfg.RateLimit.GlobalDownloads)
	lim.Add("qq:api", cfg.NapCat.MaxConcurrentRequests)
	p.limiter = lim
}

func (p *Pool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	workers := p.cfg.Worker.MaxActiveTasks
	if workers <= 0 {
		workers = 8
	}
	for i := 0; i < workers; i++ {
		if p.queue != nil {
			go p.runRedis(ctx, i)
		} else {
			go p.run(ctx, i)
		}
	}
}

func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

func (p *Pool) runRedis(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		msgs, err := p.queue.Read(ctx, 5*time.Second, 4)
		if err != nil {
			log.Printf("worker %d redis read: %v", id, err)
			time.Sleep(time.Second)
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		for _, msg := range msgs {
			task, err := p.repo.ClaimTask(ctx, msg.TaskID)
			if err != nil {
				log.Printf("worker %d claim after redis message %s: %v", id, msg.ID, err)
				continue
			}
			if task == nil {
				_ = p.queue.Ack(ctx, msg.ID)
				continue
			}
			if err := p.handle(ctx, task); err != nil {
				log.Printf("task %d failed: %v", task.ID, err)
				_ = p.repo.MarkFailedOrRetry(ctx, task, err)
			} else {
				_ = p.repo.MarkDone(ctx, task)
			}
			_ = p.queue.Ack(ctx, msg.ID)
		}
	}
}

func (p *Pool) run(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		task, err := p.repo.ClaimNext(ctx)
		if err != nil {
			log.Printf("worker %d claim: %v", id, err)
			time.Sleep(time.Second)
			continue
		}
		if task == nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err := p.handle(ctx, task); err != nil {
			log.Printf("task %d failed: %v", task.ID, err)
			_ = p.repo.MarkFailedOrRetry(ctx, task, err)
			continue
		}
		_ = p.repo.MarkDone(ctx, task)
	}
}

func (p *Pool) handle(ctx context.Context, t *repository.Task) error {
	switch t.TaskType {
	case repository.TaskWebToStorage:
		return p.handleWebToStorage(ctx, t)
	case repository.TaskWebToQQ:
		return p.handleWebToQQ(ctx, t)
	case repository.TaskQQToStorage:
		return p.handleQQToStorage(ctx, t)
	case repository.TaskQQToQQ:
		return p.handleQQToForward(ctx, t)
	default:
		return fmt.Errorf("unknown task type %s", t.TaskType)
	}
}

func (p *Pool) handleWebToStorage(ctx context.Context, t *repository.Task) error {
	if t.SourceType == "local" {
		if _, err := os.Stat(t.TargetStoragePath); err != nil {
			return fmt.Errorf("local file missing: %s: %w", t.TargetStoragePath, err)
		}
		return nil
	}
	res, err := p.download(ctx, t.SourceURL, t.FileName, t.ID)
	if err != nil {
		return err
	}
	defer os.Remove(res.Path)
	t.SHA256, t.FileSize, t.ContentType = res.SHA256, res.Size, res.ContentType
	path, err := p.storage.PutFile(ctx, res.Path, t.FileName)
	if err != nil {
		return err
	}
	t.TargetStoragePath = path
	return nil
}

func (p *Pool) handleWebToQQ(ctx context.Context, t *repository.Task) error {
	var filePath string
	if t.SourceType == "local" {
		filePath = t.TargetStoragePath
		if _, err := os.Stat(filePath); err != nil {
			return fmt.Errorf("local file missing: %s: %w", filePath, err)
		}
	} else {
		res, err := p.download(ctx, t.SourceURL, t.FileName, t.ID)
		if err != nil {
			return err
		}
		filePath = res.Path
		defer os.Remove(res.Path)
		t.SHA256, t.FileSize, t.ContentType = res.SHA256, res.Size, res.ContentType
	}
	release, err := p.limiter.Wait(ctx, fmt.Sprintf("group:upload:%d", t.TargetGroupID))
	if err != nil {
		return err
	}
	defer release()
	return p.sendFileForward(ctx, t.TargetGroupID, filePath, t.FileName, fmt.Sprintf("来源: %s", t.SourceURL))
}

func (p *Pool) handleQQToStorage(ctx context.Context, t *repository.Task) error {
	release, err := p.limiter.Wait(ctx, "qq:api")
	if err != nil {
		return err
	}
	url, err := p.napcat.GetGroupFileURL(ctx, t.SourceGroupID, t.SourceFileID, t.SourceBusID)
	release()
	if err != nil {
		return err
	}
	res, err := p.download(ctx, url, t.FileName, t.ID)
	if err != nil {
		return err
	}
	defer os.Remove(res.Path)
	t.SHA256, t.FileSize = res.SHA256, res.Size
	path, err := p.storage.PutFile(ctx, res.Path, t.FileName)
	if err != nil {
		return err
	}
	t.TargetStoragePath = path
	return nil
}

func (p *Pool) handleQQToQQ(ctx context.Context, t *repository.Task) error {
	release, err := p.limiter.Wait(ctx, "qq:api")
	if err != nil {
		return err
	}
	defer release()
	return p.napcat.TransGroupFile(ctx, t.SourceGroupID, t.TargetGroupID, t.SourceFileID, t.SourceBusID)
}

func (p *Pool) handleQQToForward(ctx context.Context, t *repository.Task) error {
	if err := p.handleQQToStorage(ctx, t); err != nil {
		return err
	}
	release, err := p.limiter.Wait(ctx, fmt.Sprintf("group:upload:%d", t.TargetGroupID))
	if err != nil {
		return err
	}
	defer release()
	source := fmt.Sprintf("来源: QQ群 %d", t.SourceGroupID)
	if t.SourceFolderID != "" {
		source += " / " + t.SourceFolderID
	}
	return p.sendFileForward(ctx, t.TargetGroupID, t.TargetStoragePath, t.FileName, source)
}

func (p *Pool) sendFileForward(ctx context.Context, groupID int64, filePath, fileName, source string) error {
	if source == "" {
		source = "来源: NapCatFileMover"
	}
	nodes := []napcat.ForwardNode{
		{
			UserID:   "10000",
			Nickname: "NapCatFileMover",
			Content: []napcat.MessageSegment{{
				Type: "text",
				Data: map[string]any{"text": fmt.Sprintf("搬运资料: %s\n%s", fileName, source)},
			}},
		},
		{
			UserID:   "10000",
			Nickname: "NapCatFileMover",
			Content: []napcat.MessageSegment{{
				Type: "file",
				Data: map[string]any{"file": filePath, "name": fileName},
			}},
		},
	}
	return p.napcat.SendGroupForwardMsg(ctx, groupID, nodes)
}

func (p *Pool) download(ctx context.Context, rawURL, name string, taskID int64) (downloader.Result, error) {
	release, err := p.limiter.Wait(ctx, "global:download")
	if err != nil {
		return downloader.Result{}, err
	}
	defer release()
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		hostRelease, err := p.limiter.Wait(ctx, "host:"+u.Hostname())
		if err != nil {
			return downloader.Result{}, err
		}
		defer hostRelease()
	}
	name = security.SanitizeFilename(name)
	if name == "unnamed" {
		name = fmt.Sprintf("task-%d.bin", taskID)
	}
	dst := filepath.Join(p.cfg.Paths.CacheDir, fmt.Sprintf("%d-%s", taskID, name))
	return p.downloader.Download(ctx, rawURL, dst)
}

func Idempotency(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
