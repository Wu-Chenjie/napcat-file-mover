<script>
  let token = '';
  let authed = false;
  let tasks = [];
  let searchQuery = '';
  let searchResults = [];
  let searchId = '';
  let target = 'storage';
  let status = '';
  let error = '';
  let notice = '';
  let active = 'tasks';
  let settings = null;
  let adminsText = '';
  let groupsText = '';
  let hostsText = '';
  let newAdminToken = '';
  let newNapCatToken = '';

  async function api(path, options = {}) {
    const res = await fetch(path, {
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
      ...options
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }

  async function login() {
    error = '';
    try {
      await api('/api/auth/login', { method: 'POST', body: JSON.stringify({ token }) });
      authed = true;
      await loadTasks();
      await loadSettings();
      connectEvents();
    } catch (err) {
      error = err.message;
    }
  }

  async function loadTasks() {
    const params = status ? `?status=${encodeURIComponent(status)}` : '';
    const data = await api(`/api/tasks${params}`);
    tasks = data.tasks || [];
  }

  async function act(id, action) {
    await api(`/api/tasks/${id}/${action}`, { method: 'POST', body: '{}' });
    await loadTasks();
  }

  async function searchFiles() {
    error = '';
    const data = await api(`/api/search/files?q=${encodeURIComponent(searchQuery)}&limit=100`);
    searchResults = data.results || [];
    searchId = data.search_id;
  }

  async function transferSearch() {
    const data = await api(`/api/search/${searchId}/transfer`, {
      method: 'POST',
      body: JSON.stringify({ target })
    });
    await loadTasks();
    alert(`已创建 ${data.created} 个任务`);
  }

  async function loadSettings() {
    settings = await api('/api/config');
    adminsText = (settings.bot.admins || []).join('\n');
    groupsText = (settings.bot.allowed_groups || []).join('\n');
    hostsText = (settings.website.allowed_hosts || []).join('\n');
    newAdminToken = '';
    newNapCatToken = '';
  }

  async function saveSettings() {
    error = '';
    notice = '';
    const payload = structuredClone(settings);
    payload.bot.admins = parseIntLines(adminsText);
    payload.bot.allowed_groups = parseIntLines(groupsText);
    payload.website.allowed_hosts = parseTextLines(hostsText);
    payload.app.admin_token = newAdminToken.trim();
    payload.napcat.token = newNapCatToken.trim();
    const data = await api('/api/config', {
      method: 'PUT',
      body: JSON.stringify(payload)
    });
    settings = data.config;
    newAdminToken = '';
    newNapCatToken = '';
    notice = data.restart_required ? '配置已保存，重启服务后完全生效。' : '配置已保存。';
  }

  function connectEvents() {
    const events = new EventSource('/api/events', { withCredentials: true });
    events.addEventListener('snapshot', (event) => {
      const data = JSON.parse(event.data);
      tasks = data.tasks || tasks;
    });
  }

  function formatSize(size) {
    if (!size) return '-';
    if (size > 1024 * 1024 * 1024) return `${(size / 1024 / 1024 / 1024).toFixed(2)} GB`;
    if (size > 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`;
    return `${(size / 1024).toFixed(1)} KB`;
  }

  function parseTextLines(value) {
    return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  }

  function parseIntLines(value) {
    return parseTextLines(value).map((line) => Number.parseInt(line, 10)).filter((value) => Number.isFinite(value));
  }
</script>

{#if !authed}
  <main class="login">
    <section class="login-panel">
      <h1>NapCat File Mover</h1>
      <input bind:value={token} type="password" placeholder="Admin token" on:keydown={(e) => e.key === 'Enter' && login()} />
      <button on:click={login}>登录</button>
      {#if error}<p class="error">{error}</p>{/if}
    </section>
  </main>
{:else}
  <main class="shell">
    <aside>
      <h1>File Mover</h1>
      <button class:active={active === 'tasks'} on:click={() => active = 'tasks'}>任务</button>
      <button class:active={active === 'search'} on:click={() => active = 'search'}>搜索</button>
      <button class:active={active === 'settings'} on:click={() => active = 'settings'}>设置</button>
      <button on:click={loadTasks}>刷新任务</button>
      <select bind:value={status} on:change={loadTasks}>
        <option value="">全部状态</option>
        <option value="pending">pending</option>
        <option value="queued">queued</option>
        <option value="downloading">downloading</option>
        <option value="uploading">uploading</option>
        <option value="done">done</option>
        <option value="failed">failed</option>
        <option value="paused">paused</option>
      </select>
    </aside>

    <section class="content">
      {#if notice}<p class="notice">{notice}</p>{/if}
      {#if error}<p class="error">{error}</p>{/if}

      {#if active === 'search'}
        <section class="toolbar">
          <input bind:value={searchQuery} placeholder="输入主题搜索文件" on:keydown={(e) => e.key === 'Enter' && searchFiles()} />
          <button on:click={searchFiles}>搜索</button>
          <input bind:value={target} placeholder="storage 或目标群号" />
          <button disabled={!searchId} on:click={transferSearch}>批量搬运</button>
        </section>

        <section class="panel grow">
          <h2>搜索结果</h2>
          <div class="table">
            {#each searchResults as row}
              <div class="tr">
                <span>{row.file_name}</span>
                <span>{row.group_id}</span>
                <span>{formatSize(row.file_size)}</span>
                <span>{row.score.toFixed(2)}</span>
                <span>{row.reason}</span>
              </div>
            {/each}
          </div>
        </section>
      {:else if active === 'settings' && settings}
        <section class="panel settings">
          <h2>设置</h2>
          <div class="settings-grid">
            <label>监听地址<input bind:value={settings.server.listen} /></label>
            <label>NapCat 地址<input bind:value={settings.napcat.endpoint} /></label>
            <label>NapCat Token<input bind:value={newNapCatToken} type="password" placeholder={settings.napcat.token_set ? '已设置，留空不修改' : '未设置'} /></label>
            <label>Admin Token<input bind:value={newAdminToken} type="password" placeholder={settings.app.admin_token_set ? '已设置，留空不修改' : '未设置'} /></label>
            <label>NapCat 超时秒<input bind:value={settings.napcat.timeout_seconds} type="number" min="1" /></label>
            <label>NapCat 并发<input bind:value={settings.napcat.max_concurrent_requests} type="number" min="1" /></label>
            <label>最大文件 MB<input bind:value={settings.website.max_file_size_mb} type="number" min="1" /></label>
            <label>本地存储目录<input bind:value={settings.storage.local_root} /></label>
            <label>下载 Worker<input bind:value={settings.worker.download_workers} type="number" min="1" /></label>
            <label>最大活跃任务<input bind:value={settings.worker.max_active_tasks} type="number" min="1" /></label>
            <label>Buffer KB<input bind:value={settings.worker.buffer_size_kb} type="number" min="64" /></label>
            <label>最大重试<input bind:value={settings.worker.max_retries} type="number" min="0" /></label>
            <label>高置信度阈值<input bind:value={settings.search.high_confidence} type="number" min="0.1" max="1" step="0.01" /></label>
            <label>单批文件数<input bind:value={settings.search.max_batch_files} type="number" min="1" /></label>
            <label>单批总大小 MB<input bind:value={settings.search.max_batch_size_mb} type="number" min="1" /></label>
            <label>Embedding Endpoint<input bind:value={settings.search.embedding_endpoint} /></label>
            <label class="wide">管理员 QQ<textarea bind:value={adminsText} rows="5"></textarea></label>
            <label class="wide">允许群号<textarea bind:value={groupsText} rows="5"></textarea></label>
            <label class="wide">允许网站域名<textarea bind:value={hostsText} rows="5"></textarea></label>
          </div>
          <div class="settings-actions">
            <button on:click={loadSettings}>重新读取</button>
            <button on:click={saveSettings}>保存配置</button>
          </div>
          <dl class="paths">
            <dt>配置</dt><dd>{settings.paths.Config}</dd>
            <dt>数据库</dt><dd>{settings.paths.Database}</dd>
            <dt>缓存</dt><dd>{settings.paths.CacheDir}</dd>
            <dt>日志</dt><dd>{settings.paths.LogDir}</dd>
          </dl>
        </section>
      {:else}
        <section class="panel grow">
          <h2>任务</h2>
          <div class="table">
            <div class="tr head">
              <span>ID</span><span>状态</span><span>类型</span><span>文件</span><span>大小</span><span>错误</span><span>操作</span>
            </div>
            {#each tasks as task}
              <div class="tr">
                <span>#{task.id}</span>
                <span class={`badge ${task.status}`}>{task.status}</span>
                <span>{task.task_type}</span>
                <span title={task.file_name}>{task.file_name || task.source_url}</span>
                <span>{formatSize(task.file_size)}</span>
                <span title={task.last_error}>{task.last_error || '-'}</span>
                <span class="actions">
                  <button on:click={() => act(task.id, 'retry')}>重试</button>
                  <button on:click={() => act(task.id, 'pause')}>暂停</button>
                  <button on:click={() => act(task.id, 'resume')}>恢复</button>
                </span>
              </div>
            {/each}
          </div>
        </section>
      {/if}
    </section>
  </main>
{/if}
