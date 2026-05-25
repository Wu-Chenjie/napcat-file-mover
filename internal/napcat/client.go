package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	endpoint string
	token    string
	http     *http.Client
	sem      chan struct{}
}

func New(endpoint, token string, timeout time.Duration, maxConcurrent int) *Client {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	tr := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 128,
		MaxConnsPerHost:     128,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http:     &http.Client{Transport: tr, Timeout: timeout},
		sem:      make(chan struct{}, maxConcurrent),
	}
}

type LoginInfo struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
}

func (c *Client) GetLoginInfo(ctx context.Context) (LoginInfo, error) {
	var info LoginInfo
	err := c.call(ctx, "get_login_info", map[string]any{}, &info)
	return info, err
}

func (c *Client) SendGroupMsg(ctx context.Context, groupID int64, message string) error {
	var out json.RawMessage
	return c.call(ctx, "send_group_msg", map[string]any{"group_id": groupID, "message": message}, &out)
}

func (c *Client) SendGroupForwardMsg(ctx context.Context, groupID int64, nodes []ForwardNode) error {
	messages := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		messages = append(messages, map[string]any{
			"type": "node",
			"data": node,
		})
	}
	var out json.RawMessage
	return c.call(ctx, "send_group_forward_msg", map[string]any{"group_id": groupID, "messages": messages}, &out)
}

func (c *Client) GetGroupRootFiles(ctx context.Context, groupID int64) ([]QQFile, error) {
	data, err := c.GetGroupRootEntries(ctx, groupID)
	return data.Files, err
}

func (c *Client) GetGroupFilesByFolder(ctx context.Context, groupID int64, folderID string) ([]QQFile, error) {
	data, err := c.GetGroupFolderEntries(ctx, groupID, folderID)
	return data.Files, err
}

func (c *Client) GetGroupRootEntries(ctx context.Context, groupID int64) (QQFileList, error) {
	var data QQFileList
	err := c.call(ctx, "get_group_root_files", map[string]any{"group_id": groupID}, &data)
	return data, err
}

func (c *Client) GetGroupFolderEntries(ctx context.Context, groupID int64, folderID string) (QQFileList, error) {
	var data QQFileList
	err := c.call(ctx, "get_group_files_by_folder", map[string]any{"group_id": groupID, "folder_id": folderID}, &data)
	return data, err
}

func (c *Client) GetGroupFileURL(ctx context.Context, groupID int64, fileID string, busID int32) (string, error) {
	var data struct {
		URL string `json:"url"`
	}
	err := c.call(ctx, "get_group_file_url", map[string]any{"group_id": groupID, "file_id": fileID, "busid": busID}, &data)
	return data.URL, err
}

func (c *Client) UploadGroupFile(ctx context.Context, groupID int64, filePath, name, folderID string) error {
	params := map[string]any{"group_id": groupID, "file": filePath, "name": name}
	if folderID != "" {
		params["folder"] = folderID
	}
	var out json.RawMessage
	return c.call(ctx, "upload_group_file", params, &out)
}

func (c *Client) TransGroupFile(ctx context.Context, srcGroupID, dstGroupID int64, fileID string, busID int32) error {
	var out json.RawMessage
	return c.call(ctx, "trans_group_file", map[string]any{
		"group_id": srcGroupID, "target_group_id": dstGroupID, "file_id": fileID, "busid": busID,
	}, &out)
}

func (c *Client) call(ctx context.Context, action string, payload any, dest any) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/"+action, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var wrapped apiResponse[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (wrapped.Status != "" && wrapped.Status != "ok") {
		return fmt.Errorf("napcat %s failed: http=%d ret=%d %s", action, resp.StatusCode, wrapped.RetCode, wrapped.Message)
	}
	if dest == nil || len(wrapped.Data) == 0 {
		return nil
	}
	return json.Unmarshal(wrapped.Data, dest)
}
