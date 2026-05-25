# NapCat 资料搬运器

基于 Go + Svelte/Wails 的最小可行产品（MVP），用于搬运已授权的 NapCat QQ 群文件和白名单网站文件。

## 当前 MVP

- Go 核心服务，使用 SQLite 持久化。
- NapCat OneBot HTTP 回调（`POST /onebot/event`）。
- 本地缓存与归档存储。
- 任务 API，支持重试/暂停/恢复，SSE 快照，健康/就绪/指标端点。
- 基于 Svelte/Vite 的操作界面（GUI）。
- Wails 桌面壳用于 Windows/macOS 打包入口。
- 文件名清理与跨平台的应用数据路径支持。
- 使用 SQLite FTS5 的文件目录搜索，支持模糊文本、拼音和首字母回退。

## 使用教程

完整的安装、配置、NapCat 对接、GUI 操作和群命令示例见 [docs/使用教程.md](docs/使用教程.md)。

## 运行核心服务

```bash
go run ./cmd/mover
```

第一次运行会在平台的应用数据目录写入默认配置：

- macOS: `~/Library/Application Support/NapCatFileMover/config.yaml`
- Windows: `%LOCALAPPDATA%/NapCatFileMover/config.yaml`

打开 `http://127.0.0.1:8088`，使用 `app.admin_token` 登录。

## 构建界面

```bash
cd frontend
npm install
npm run build
```

然后重启 Go 服务，使其提供 `frontend/dist` 下的静态文件。

## Wails 桌面应用

```bash
wails dev
wails build
```

Wails 应用会启动相同的本地 Go 服务，并绑定桌面操作（状态、打开日志目录、打开配置目录）。

## NapCat

请将 NapCat OneBot 的 HTTP 事件上报配置为：

```text
http://127.0.0.1:8088/onebot/event
```

支持的群命令：

```text
/搬运网页 <URL> <目标群号|storage>
/搜索文件 <主题>
/搬运主题 <主题> <目标群号|storage>
/任务状态 <任务ID>
/重试任务 <任务ID>
```

## 说明

语义搜索作为可选的 sidecar 集成点。如果未配置本地嵌入服务，搬运器仍然可以使用文件名、文件夹、拼音、首字母和模糊搜索工作。
