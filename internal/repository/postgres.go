package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Postgres struct {
	db *sql.DB
}

func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("database.dsn is required for postgres")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	repo := &Postgres{db: db}
	if err := repo.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return repo, nil
}

func (p *Postgres) Close() error { return p.db.Close() }

func (p *Postgres) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id BIGSERIAL PRIMARY KEY,
			task_type TEXT NOT NULL,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL DEFAULT 100,
			source_type TEXT NOT NULL,
			source_group_id BIGINT,
			source_url TEXT,
			source_file_id TEXT,
			source_bus_id INTEGER,
			source_folder_id TEXT,
			target_type TEXT NOT NULL,
			target_group_id BIGINT,
			target_folder_id TEXT,
			target_storage_path TEXT,
			file_name TEXT,
			file_size BIGINT,
			content_type TEXT,
			sha256 TEXT,
			idempotency_key TEXT NOT NULL UNIQUE,
			retry_count INTEGER NOT NULL DEFAULT 0,
			max_retries INTEGER NOT NULL DEFAULT 5,
			last_error TEXT,
			created_by BIGINT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			started_at TIMESTAMPTZ,
			finished_at TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_priority ON tasks(status, priority, created_at)`,
		`CREATE TABLE IF NOT EXISTS files (
			id BIGSERIAL PRIMARY KEY,
			sha256 TEXT NOT NULL,
			file_size BIGINT NOT NULL,
			file_name TEXT NOT NULL,
			storage_path TEXT,
			first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(sha256, file_size)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id BIGSERIAL PRIMARY KEY,
			operator_qq BIGINT,
			group_id BIGINT,
			command TEXT,
			action TEXT NOT NULL,
			result TEXT NOT NULL,
			ip TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS file_catalog (
			id BIGSERIAL PRIMARY KEY,
			group_id BIGINT NOT NULL,
			folder_id TEXT,
			folder_path TEXT,
			file_id TEXT NOT NULL,
			bus_id INTEGER NOT NULL,
			file_name TEXT NOT NULL,
			ext TEXT,
			file_size BIGINT NOT NULL,
			normalized_text TEXT,
			pinyin TEXT,
			initials TEXT,
			ngrams TEXT,
			embedding_json TEXT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(group_id, file_id, bus_id, file_size)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := p.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := p.db.ExecContext(ctx, `UPDATE tasks SET status = 'pending', last_error = COALESCE(last_error, '') || ' recovered after restart'
		WHERE status IN ('queued', 'downloading', 'uploading', 'verifying')`)
	return err
}

func (p *Postgres) CreateTask(ctx context.Context, t *Task) (int64, error) {
	if t.Priority == 0 {
		t.Priority = 100
	}
	if t.Status == "" {
		t.Status = StatusPending
	}
	if t.MaxRetries == 0 {
		t.MaxRetries = 5
	}
	var id int64
	err := p.db.QueryRowContext(ctx, `INSERT INTO tasks (
		task_type, status, priority, source_type, source_group_id, source_url, source_file_id, source_bus_id, source_folder_id,
		target_type, target_group_id, target_folder_id, target_storage_path, file_name, file_size, content_type, sha256,
		idempotency_key, max_retries, created_by
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	ON CONFLICT(idempotency_key) DO NOTHING RETURNING id`,
		t.TaskType, t.Status, t.Priority, t.SourceType, zeroNil(t.SourceGroupID), blankNil(t.SourceURL), blankNil(t.SourceFileID), zeroNil32(t.SourceBusID), blankNil(t.SourceFolderID),
		t.TargetType, zeroNil(t.TargetGroupID), blankNil(t.TargetFolderID), blankNil(t.TargetStoragePath), blankNil(t.FileName), zeroNil(t.FileSize), blankNil(t.ContentType), blankNil(t.SHA256),
		t.IdempotencyKey, t.MaxRetries, zeroNil(t.CreatedBy)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (p *Postgres) ClaimNext(ctx context.Context) (*Task, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE status = 'pending' ORDER BY priority ASC, created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'queued', updated_at = NOW(), started_at = COALESCE(started_at, NOW()) WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p.GetTask(ctx, id)
}

func (p *Postgres) ClaimTask(ctx context.Context, id int64) (*Task, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'queued', updated_at = NOW(),
		started_at = COALESCE(started_at, NOW()) WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return nil, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p.GetTask(ctx, id)
}

func (p *Postgres) GetTask(ctx context.Context, id int64) (*Task, error) {
	row := p.db.QueryRowContext(ctx, postgresTaskSelect()+` WHERE id = $1`, id)
	return scanTask(row)
}

func (p *Postgres) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	where := []string{"1=1"}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Status != "" {
		where = append(where, "status = "+add(f.Status))
	}
	if f.Type != "" {
		where = append(where, "task_type = "+add(f.Type))
	}
	if f.GroupID != 0 {
		a, b := add(f.GroupID), add(f.GroupID)
		where = append(where, "(source_group_id = "+a+" OR target_group_id = "+b+")")
	}
	if f.Query != "" {
		q := "%" + f.Query + "%"
		a, b, c := add(q), add(q), add(q)
		where = append(where, "(file_name ILIKE "+a+" OR source_url ILIKE "+b+" OR last_error ILIKE "+c+")")
	}
	limit, offset := add(f.Limit), add(f.Offset)
	rows, err := p.db.QueryContext(ctx, postgresTaskSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT `+limit+` OFFSET `+offset, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (p *Postgres) SetTaskStatus(ctx context.Context, id int64, status TaskStatus, lastError string) error {
	_, err := p.db.ExecContext(ctx, `UPDATE tasks SET status = $1, last_error = $2, updated_at = NOW(),
		finished_at = CASE WHEN $3 IN ('done', 'failed', 'canceled') THEN NOW() ELSE finished_at END WHERE id = $4`,
		status, blankNil(lastError), string(status), id)
	return err
}

func (p *Postgres) MarkDone(ctx context.Context, t *Task) error {
	_, err := p.db.ExecContext(ctx, `UPDATE tasks SET status = 'done', sha256 = $1, file_size = $2, target_storage_path = $3,
		updated_at = NOW(), finished_at = NOW() WHERE id = $4`, blankNil(t.SHA256), zeroNil(t.FileSize), blankNil(t.TargetStoragePath), t.ID)
	if err == nil && t.SHA256 != "" && t.FileSize > 0 {
		_, _ = p.db.ExecContext(ctx, `INSERT INTO files(sha256, file_size, file_name, storage_path) VALUES($1,$2,$3,$4)
			ON CONFLICT(sha256, file_size) DO UPDATE SET last_seen_at = NOW(), storage_path = COALESCE(excluded.storage_path, files.storage_path)`,
			t.SHA256, t.FileSize, t.FileName, blankNil(t.TargetStoragePath))
	}
	return err
}

func (p *Postgres) MarkFailedOrRetry(ctx context.Context, t *Task, cause error) error {
	msg := cause.Error()
	if t.RetryCount+1 <= t.MaxRetries {
		_, err := p.db.ExecContext(ctx, `UPDATE tasks SET status = 'pending', retry_count = retry_count + 1, last_error = $1, updated_at = NOW() WHERE id = $2`, msg, t.ID)
		return err
	}
	_, err := p.db.ExecContext(ctx, `UPDATE tasks SET status = 'failed', retry_count = retry_count + 1, last_error = $1, updated_at = NOW(), finished_at = NOW() WHERE id = $2`, msg, t.ID)
	return err
}

func (p *Postgres) RetryTask(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx, `UPDATE tasks SET status = 'pending', last_error = NULL, updated_at = NOW(), finished_at = NULL WHERE id = $1 AND status IN ('failed', 'paused')`, id)
	return err
}

func (p *Postgres) UpsertCatalog(ctx context.Context, f FileCatalog) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO file_catalog(group_id, folder_id, folder_path, file_id, bus_id, file_name, ext, file_size, normalized_text, pinyin, initials, ngrams, embedding_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT(group_id, file_id, bus_id, file_size) DO UPDATE SET
		folder_id = excluded.folder_id, folder_path = excluded.folder_path, file_name = excluded.file_name, ext = excluded.ext,
		normalized_text = excluded.normalized_text, pinyin = excluded.pinyin, initials = excluded.initials, ngrams = excluded.ngrams,
		embedding_json = COALESCE(excluded.embedding_json, file_catalog.embedding_json), updated_at = NOW()`,
		f.GroupID, blankNil(f.FolderID), blankNil(f.FolderPath), f.FileID, f.BusID, f.FileName, blankNil(f.Ext), f.FileSize,
		f.NormalizedText, f.Pinyin, f.Initials, f.NGrams, blankNil(f.EmbeddingJSON))
	return err
}

func (p *Postgres) IndexLocalFile(ctx context.Context, f FileCatalog) error {
	f.GroupID = 0
	f.BusID = 0
	if strings.TrimSpace(f.FileID) == "" {
		return nil
	}
	return p.UpsertCatalog(ctx, f)
}

func (p *Postgres) FindLocalByName(ctx context.Context, name string) (*FileCatalog, error) {
	row := p.db.QueryRowContext(ctx, postgresCatalogSelect()+` WHERE group_id = 0 AND file_name = $1 LIMIT 1`, name)
	return scanCatalog(row)
}

func (p *Postgres) SearchFiles(ctx context.Context, query string, groupID int64, ext string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{}
	where := []string{"(file_name ILIKE $1 OR folder_path ILIKE $1 OR normalized_text ILIKE $1 OR pinyin ILIKE $1 OR initials ILIKE $1 OR ngrams ILIKE $1)"}
	args = append(args, "%"+query+"%")
	if groupID != 0 {
		args = append(args, groupID)
		where = append(where, fmt.Sprintf("group_id = $%d", len(args)))
	}
	if ext != "" {
		args = append(args, ext)
		where = append(where, fmt.Sprintf("ext = $%d", len(args)))
	}
	args = append(args, limit)
	rows, err := p.db.QueryContext(ctx, postgresCatalogSelect()+` WHERE `+strings.Join(where, " AND ")+fmt.Sprintf(` ORDER BY updated_at DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		f, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, SearchResult{FileCatalog: *f, Score: 0.65, TextScore: 0.65, MatchedBy: "text", Reason: "postgres-fuzzy"})
	}
	return out, rows.Err()
}

func (p *Postgres) ListCatalog(ctx context.Context, limit int) ([]FileCatalog, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := p.db.QueryContext(ctx, postgresCatalogSelect()+` ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileCatalog
	for rows.Next() {
		f, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (p *Postgres) UpdateCatalogEmbedding(ctx context.Context, id int64, embeddingJSON string) error {
	_, err := p.db.ExecContext(ctx, `UPDATE file_catalog SET embedding_json = $1, updated_at = NOW() WHERE id = $2`, embeddingJSON, id)
	return err
}

func (p *Postgres) PendingTaskIDs(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM tasks WHERE status = 'pending' ORDER BY priority ASC, created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (p *Postgres) Audit(ctx context.Context, operatorQQ, groupID int64, command, action, result, ip string) {
	_, _ = p.db.ExecContext(ctx, `INSERT INTO audit_logs(operator_qq, group_id, command, action, result, ip) VALUES($1,$2,$3,$4,$5,$6)`,
		zeroNil(operatorQQ), zeroNil(groupID), command, action, result, ip)
}

func postgresTaskSelect() string {
	return `SELECT id, task_type, status, priority, source_type, COALESCE(source_group_id, 0), COALESCE(source_url, ''),
		COALESCE(source_file_id, ''), COALESCE(source_bus_id, 0), COALESCE(source_folder_id, ''), target_type,
		COALESCE(target_group_id, 0), COALESCE(target_folder_id, ''), COALESCE(target_storage_path, ''),
		COALESCE(file_name, ''), COALESCE(file_size, 0), COALESCE(content_type, ''), COALESCE(sha256, ''),
		idempotency_key, retry_count, max_retries, COALESCE(last_error, ''), COALESCE(created_by, 0),
		created_at, updated_at, started_at, finished_at FROM tasks`
}

func postgresCatalogSelect() string {
	return `SELECT id, group_id, COALESCE(folder_id, ''), COALESCE(folder_path, ''), file_id, bus_id, file_name, COALESCE(ext, ''), file_size,
		COALESCE(normalized_text, ''), COALESCE(pinyin, ''), COALESCE(initials, ''), COALESCE(ngrams, ''), COALESCE(embedding_json, ''), updated_at FROM file_catalog`
}
