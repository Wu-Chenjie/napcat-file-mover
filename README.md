# NapCat File Mover

Go + Svelte/Wails MVP for moving authorized NapCat QQ group files and whitelisted website files.

## Current MVP

- Go core service with SQLite persistence.
- NapCat OneBot HTTP callback at `POST /onebot/event`.
- Local cache and local archive storage.
- Task API, retry/pause/resume, SSE snapshots, health/readiness/metrics.
- Svelte/Vite operations GUI.
- Wails desktop shell entrypoint for Windows/macOS packaging.
- Filename sanitization and cross-platform application data paths.
- File catalog search with SQLite FTS5, fuzzy text, pinyin, and initials fallback.

## 使用教程

完整的安装、配置、NapCat 对接、GUI 操作和群命令示例见 [docs/使用教程.md](docs/使用教程.md)。

## Run Core Service

```bash
go run ./cmd/mover
```

The first run writes a default config into the platform app data directory:

- macOS: `~/Library/Application Support/NapCatFileMover/config.yaml`
- Windows: `%LOCALAPPDATA%/NapCatFileMover/config.yaml`

Open `http://127.0.0.1:8088` and log in with `app.admin_token`.

## Build GUI

```bash
cd frontend
npm install
npm run build
```

Then restart the Go service so it serves `frontend/dist`.

## Wails Desktop

```bash
wails dev
wails build
```

The Wails app starts the same local Go service and binds desktop actions for status, opening the log directory, and opening the config directory.

## NapCat

Configure NapCat OneBot HTTP event reporting to:

```text
http://127.0.0.1:8088/onebot/event
```

Supported group commands:

```text
/搬运网页 <URL> <目标群号|storage>
/搜索文件 <主题>
/搬运主题 <主题> <目标群号|storage>
/任务状态 <任务ID>
/重试任务 <任务ID>
```

## Notes

Embedding semantic search is intentionally a sidecar integration point. If no local embedding service is configured, the mover continues to work with filename, folder, pinyin, initials, and fuzzy search.
