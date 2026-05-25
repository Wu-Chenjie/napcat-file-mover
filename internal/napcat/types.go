package napcat

type QQFile struct {
	GroupID    int64  `json:"group_id,omitempty"`
	FileID     string `json:"file_id"`
	BusID      int32  `json:"busid,omitempty"`
	FileName   string `json:"file_name"`
	FileSize   int64  `json:"file_size"`
	FolderID   string `json:"folder_id,omitempty"`
	FolderName string `json:"folder_name,omitempty"`
}

type QQFolder struct {
	GroupID    int64  `json:"group_id,omitempty"`
	FolderID   string `json:"folder_id"`
	FolderName string `json:"folder_name"`
}

type QQFileList struct {
	Files   []QQFile   `json:"files"`
	Folders []QQFolder `json:"folders"`
}

type OneBotEvent struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
	GroupID     int64  `json:"group_id"`
	UserID      int64  `json:"user_id"`
	Message     any    `json:"message"`
	RawMessage  string `json:"raw_message"`
}

type MessageSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type ForwardNode struct {
	UserID   string           `json:"user_id"`
	Nickname string           `json:"nickname"`
	Content  []MessageSegment `json:"content"`
}

type apiResponse[T any] struct {
	Status  string `json:"status"`
	RetCode int    `json:"retcode"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}
