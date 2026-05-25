package repository

import "time"

type TaskType string

const (
	TaskQQToStorage  TaskType = "qq_to_storage"
	TaskWebToQQ      TaskType = "web_to_qq"
	TaskQQToQQ       TaskType = "qq_to_qq"
	TaskWebToStorage TaskType = "web_to_storage"
)

type TaskStatus string

const (
	StatusPending     TaskStatus = "pending"
	StatusQueued      TaskStatus = "queued"
	StatusDownloading TaskStatus = "downloading"
	StatusUploading   TaskStatus = "uploading"
	StatusVerifying   TaskStatus = "verifying"
	StatusDone        TaskStatus = "done"
	StatusFailed      TaskStatus = "failed"
	StatusPaused      TaskStatus = "paused"
	StatusCanceled    TaskStatus = "canceled"
)

type Task struct {
	ID                int64      `json:"id"`
	TaskType          TaskType   `json:"task_type"`
	Status            TaskStatus `json:"status"`
	Priority          int        `json:"priority"`
	SourceType        string     `json:"source_type"`
	SourceGroupID     int64      `json:"source_group_id,omitempty"`
	SourceURL         string     `json:"source_url,omitempty"`
	SourceFileID      string     `json:"source_file_id,omitempty"`
	SourceBusID       int32      `json:"source_bus_id,omitempty"`
	SourceFolderID    string     `json:"source_folder_id,omitempty"`
	TargetType        string     `json:"target_type"`
	TargetGroupID     int64      `json:"target_group_id,omitempty"`
	TargetFolderID    string     `json:"target_folder_id,omitempty"`
	TargetStoragePath string     `json:"target_storage_path,omitempty"`
	FileName          string     `json:"file_name,omitempty"`
	FileSize          int64      `json:"file_size,omitempty"`
	ContentType       string     `json:"content_type,omitempty"`
	SHA256            string     `json:"sha256,omitempty"`
	IdempotencyKey    string     `json:"idempotency_key"`
	RetryCount        int        `json:"retry_count"`
	MaxRetries        int        `json:"max_retries"`
	LastError         string     `json:"last_error,omitempty"`
	CreatedBy         int64      `json:"created_by,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

type FileCatalog struct {
	ID             int64     `json:"id"`
	GroupID        int64     `json:"group_id"`
	FolderID       string    `json:"folder_id"`
	FolderPath     string    `json:"folder_path"`
	FileID         string    `json:"file_id"`
	BusID          int32     `json:"bus_id"`
	FileName       string    `json:"file_name"`
	Ext            string    `json:"ext"`
	FileSize       int64     `json:"file_size"`
	NormalizedText string    `json:"normalized_text"`
	Pinyin         string    `json:"pinyin"`
	Initials       string    `json:"initials"`
	NGrams         string    `json:"ngrams"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type SearchResult struct {
	FileCatalog
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

type TaskFilter struct {
	Status  string
	Type    string
	Query   string
	Limit   int
	Offset  int
	GroupID int64
}
