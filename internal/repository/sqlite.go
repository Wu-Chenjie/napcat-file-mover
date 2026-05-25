package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

type SQLite struct {
	db *sql.DB
}

func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	repo := &SQLite{db: db}
	if err := repo.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return repo, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_type TEXT NOT NULL,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL DEFAULT 100,
			source_type TEXT NOT NULL,
			source_group_id INTEGER,
			source_url TEXT,
			source_file_id TEXT,
			source_bus_id INTEGER,
			source_folder_id TEXT,
			target_type TEXT NOT NULL,
			target_group_id INTEGER,
			target_folder_id TEXT,
			target_storage_path TEXT,
			file_name TEXT,
			file_size INTEGER,
			content_type TEXT,
			sha256 TEXT,
			idempotency_key TEXT NOT NULL UNIQUE,
			retry_count INTEGER NOT NULL DEFAULT 0,
			max_retries INTEGER NOT NULL DEFAULT 5,
			last_error TEXT,
			created_by INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at DATETIME,
			finished_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_priority ON tasks(status, priority, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_source_group ON tasks(source_group_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_target_group ON tasks(target_group_id)`,
		`CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sha256 TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			file_name TEXT NOT NULL,
			storage_path TEXT,
			first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(sha256, file_size)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			operator_qq INTEGER,
			group_id INTEGER,
			command TEXT,
			action TEXT NOT NULL,
			result TEXT NOT NULL,
			ip TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS file_catalog (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id INTEGER NOT NULL,
			folder_id TEXT,
			folder_path TEXT,
			file_id TEXT NOT NULL,
			bus_id INTEGER NOT NULL,
			file_name TEXT NOT NULL,
			ext TEXT,
			file_size INTEGER NOT NULL,
			normalized_text TEXT,
			pinyin TEXT,
			initials TEXT,
			ngrams TEXT,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(group_id, file_id, bus_id, file_size)
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS file_catalog_fts USING fts5(
			file_name, folder_path, normalized_text, pinyin, initials, ngrams,
			content='file_catalog', content_rowid='id'
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = 'pending', last_error = COALESCE(last_error, '') || ' recovered after restart'
		WHERE status IN ('queued', 'downloading', 'uploading', 'verifying')`)
	return err
}

func (s *SQLite) CreateTask(ctx context.Context, t *Task) (int64, error) {
	if t.Priority == 0 {
		t.Priority = 100
	}
	if t.Status == "" {
		t.Status = StatusPending
	}
	if t.MaxRetries == 0 {
		t.MaxRetries = 5
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO tasks (
		task_type, status, priority, source_type, source_group_id, source_url, source_file_id, source_bus_id, source_folder_id,
		target_type, target_group_id, target_folder_id, target_storage_path, file_name, file_size, content_type, sha256,
		idempotency_key, max_retries, created_by
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(idempotency_key) DO NOTHING`,
		t.TaskType, t.Status, t.Priority, t.SourceType, zeroNil(t.SourceGroupID), blankNil(t.SourceURL), blankNil(t.SourceFileID), zeroNil32(t.SourceBusID), blankNil(t.SourceFolderID),
		t.TargetType, zeroNil(t.TargetGroupID), blankNil(t.TargetFolderID), blankNil(t.TargetStoragePath), blankNil(t.FileName), zeroNil(t.FileSize), blankNil(t.ContentType), blankNil(t.SHA256),
		t.IdempotencyKey, t.MaxRetries, zeroNil(t.CreatedBy),
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *SQLite) ClaimNext(ctx context.Context) (*Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE status = 'pending' ORDER BY priority ASC, created_at ASC LIMIT 1`)
	var id int64
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'queued', updated_at = CURRENT_TIMESTAMP, started_at = COALESCE(started_at, CURRENT_TIMESTAMP) WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}

func (s *SQLite) GetTask(ctx context.Context, id int64) (*Task, error) {
	rows, err := s.db.QueryContext(ctx, baseTaskSelect()+` WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanTask(rows)
	}
	return nil, sql.ErrNoRows
}

func (s *SQLite) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	where := []string{"1=1"}
	args := []any{}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Type != "" {
		where = append(where, "task_type = ?")
		args = append(args, f.Type)
	}
	if f.GroupID != 0 {
		where = append(where, "(source_group_id = ? OR target_group_id = ?)")
		args = append(args, f.GroupID, f.GroupID)
	}
	if f.Query != "" {
		where = append(where, "(file_name LIKE ? OR source_url LIKE ? OR last_error LIKE ?)")
		q := "%" + f.Query + "%"
		args = append(args, q, q, q)
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, baseTaskSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *SQLite) SetTaskStatus(ctx context.Context, id int64, status TaskStatus, lastError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP,
		finished_at = CASE WHEN ? IN ('done', 'failed', 'canceled') THEN CURRENT_TIMESTAMP ELSE finished_at END
		WHERE id = ?`, status, blankNil(lastError), string(status), id)
	return err
}

func (s *SQLite) MarkDone(ctx context.Context, t *Task) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = 'done', sha256 = ?, file_size = ?, target_storage_path = ?,
		updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP WHERE id = ?`,
		blankNil(t.SHA256), zeroNil(t.FileSize), blankNil(t.TargetStoragePath), t.ID)
	if err == nil && t.SHA256 != "" && t.FileSize > 0 {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO files(sha256, file_size, file_name, storage_path) VALUES(?, ?, ?, ?)
			ON CONFLICT(sha256, file_size) DO UPDATE SET last_seen_at = CURRENT_TIMESTAMP, storage_path = COALESCE(excluded.storage_path, files.storage_path)`,
			t.SHA256, t.FileSize, t.FileName, blankNil(t.TargetStoragePath))
	}
	return err
}

func (s *SQLite) MarkFailedOrRetry(ctx context.Context, t *Task, cause error) error {
	msg := cause.Error()
	if t.RetryCount+1 <= t.MaxRetries {
		_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = 'pending', retry_count = retry_count + 1, last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, msg, t.ID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = 'failed', retry_count = retry_count + 1, last_error = ?, updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP WHERE id = ?`, msg, t.ID)
	return err
}

func (s *SQLite) RetryTask(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = 'pending', last_error = NULL, updated_at = CURRENT_TIMESTAMP, finished_at = NULL WHERE id = ? AND status IN ('failed', 'paused')`, id)
	return err
}

func (s *SQLite) UpsertCatalog(ctx context.Context, f FileCatalog) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO file_catalog(group_id, folder_id, folder_path, file_id, bus_id, file_name, ext, file_size, normalized_text, pinyin, initials, ngrams)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(group_id, file_id, bus_id, file_size) DO UPDATE SET
		folder_id = excluded.folder_id, folder_path = excluded.folder_path, file_name = excluded.file_name, ext = excluded.ext,
		normalized_text = excluded.normalized_text, pinyin = excluded.pinyin, initials = excluded.initials, ngrams = excluded.ngrams, updated_at = CURRENT_TIMESTAMP`,
		f.GroupID, blankNil(f.FolderID), blankNil(f.FolderPath), f.FileID, f.BusID, f.FileName, blankNil(f.Ext), f.FileSize, f.NormalizedText, f.Pinyin, f.Initials, f.NGrams)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		row := tx.QueryRowContext(ctx, `SELECT id FROM file_catalog WHERE group_id = ? AND file_id = ? AND bus_id = ? AND file_size = ?`, f.GroupID, f.FileID, f.BusID, f.FileSize)
		_ = row.Scan(&id)
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO file_catalog_fts(rowid, file_name, folder_path, normalized_text, pinyin, initials, ngrams)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, f.FileName, f.FolderPath, f.NormalizedText, f.Pinyin, f.Initials, f.NGrams)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) SearchFiles(ctx context.Context, query string, groupID int64, ext string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{}
	args := []any{}
	if groupID != 0 {
		where = append(where, "c.group_id = ?")
		args = append(args, groupID)
	}
	if ext != "" {
		where = append(where, "c.ext = ?")
		args = append(args, ext)
	}
	matchExpr := buildFTSQuery(query)
	baseWhere := ""
	if len(where) > 0 {
		baseWhere = " AND " + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.group_id, COALESCE(c.folder_id, ''), COALESCE(c.folder_path, ''), c.file_id, c.bus_id, c.file_name, COALESCE(c.ext, ''), c.file_size,
		c.normalized_text, c.pinyin, c.initials, c.ngrams, c.updated_at, bm25(file_catalog_fts) AS rank
		FROM file_catalog_fts JOIN file_catalog c ON c.id = file_catalog_fts.rowid
		WHERE file_catalog_fts MATCH ?`+baseWhere+` ORDER BY rank LIMIT ?`, append([]any{matchExpr}, append(args, limit)...)...)
	if err != nil {
		return s.searchLike(ctx, query, groupID, ext, limit)
	}
	defer rows.Close()
	results := []SearchResult{}
	for rows.Next() {
		f, rank, err := scanCatalogRank(rows)
		if err != nil {
			return nil, err
		}
		score := 1 / (1 + maxFloat(rank, 0))
		results = append(results, SearchResult{FileCatalog: *f, Score: score, Reason: "fts/pinyin"})
	}
	if len(results) == 0 {
		return s.searchLike(ctx, query, groupID, ext, limit)
	}
	return results, rows.Err()
}

func (s *SQLite) searchLike(ctx context.Context, query string, groupID int64, ext string, limit int) ([]SearchResult, error) {
	where := []string{"(file_name LIKE ? OR folder_path LIKE ? OR normalized_text LIKE ? OR pinyin LIKE ? OR initials LIKE ? OR ngrams LIKE ?)"}
	q := "%" + query + "%"
	args := []any{q, q, q, q, q, q}
	if groupID != 0 {
		where = append(where, "group_id = ?")
		args = append(args, groupID)
	}
	if ext != "" {
		where = append(where, "ext = ?")
		args = append(args, ext)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id, group_id, COALESCE(folder_id, ''), COALESCE(folder_path, ''), file_id, bus_id, file_name, COALESCE(ext, ''), file_size,
		normalized_text, pinyin, initials, ngrams, updated_at FROM file_catalog WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchResult{}
	for rows.Next() {
		f, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, SearchResult{FileCatalog: *f, Score: 0.65, Reason: "fuzzy"})
	}
	return out, rows.Err()
}

func (s *SQLite) Audit(ctx context.Context, operatorQQ, groupID int64, command, action, result, ip string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_logs(operator_qq, group_id, command, action, result, ip) VALUES(?, ?, ?, ?, ?, ?)`,
		zeroNil(operatorQQ), zeroNil(groupID), command, action, result, ip)
}

func baseTaskSelect() string {
	return `SELECT id, task_type, status, priority, source_type, COALESCE(source_group_id, 0), COALESCE(source_url, ''),
		COALESCE(source_file_id, ''), COALESCE(source_bus_id, 0), COALESCE(source_folder_id, ''), target_type,
		COALESCE(target_group_id, 0), COALESCE(target_folder_id, ''), COALESCE(target_storage_path, ''),
		COALESCE(file_name, ''), COALESCE(file_size, 0), COALESCE(content_type, ''), COALESCE(sha256, ''),
		idempotency_key, retry_count, max_retries, COALESCE(last_error, ''), COALESCE(created_by, 0),
		created_at, updated_at, started_at, finished_at FROM tasks`
}

type scanner interface{ Scan(dest ...any) error }

func scanTask(sc scanner) (*Task, error) {
	var t Task
	if err := sc.Scan(&t.ID, &t.TaskType, &t.Status, &t.Priority, &t.SourceType, &t.SourceGroupID, &t.SourceURL,
		&t.SourceFileID, &t.SourceBusID, &t.SourceFolderID, &t.TargetType, &t.TargetGroupID, &t.TargetFolderID,
		&t.TargetStoragePath, &t.FileName, &t.FileSize, &t.ContentType, &t.SHA256, &t.IdempotencyKey,
		&t.RetryCount, &t.MaxRetries, &t.LastError, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &t.StartedAt, &t.FinishedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func scanCatalog(sc scanner) (*FileCatalog, error) {
	var f FileCatalog
	if err := sc.Scan(&f.ID, &f.GroupID, &f.FolderID, &f.FolderPath, &f.FileID, &f.BusID, &f.FileName, &f.Ext, &f.FileSize,
		&f.NormalizedText, &f.Pinyin, &f.Initials, &f.NGrams, &f.UpdatedAt); err != nil {
		return nil, err
	}
	return &f, nil
}

func scanCatalogRank(sc scanner) (*FileCatalog, float64, error) {
	var f FileCatalog
	var rank float64
	if err := sc.Scan(&f.ID, &f.GroupID, &f.FolderID, &f.FolderPath, &f.FileID, &f.BusID, &f.FileName, &f.Ext, &f.FileSize,
		&f.NormalizedText, &f.Pinyin, &f.Initials, &f.NGrams, &f.UpdatedAt, &rank); err != nil {
		return nil, 0, err
	}
	return &f, rank, nil
}

func buildFTSQuery(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return `""`
	}
	for i, f := range fields {
		fields[i] = strings.ReplaceAll(f, `"`, "")
	}
	return fmt.Sprintf(`"%s"*`, strings.Join(fields, `" "`))
}

func blankNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func zeroNil(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func zeroNil32(v int32) any {
	if v == 0 {
		return nil
	}
	return v
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
