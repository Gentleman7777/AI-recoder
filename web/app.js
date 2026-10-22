(() => {
  'use strict';

  const $ = (selector) => document.querySelector(selector);
  const icon = (name, className = '') => `<svg class="${className}" aria-hidden="true"><use href="#ic-${name}"></use></svg>`;
  const escapeHTML = (value) => String(value ?? '').replace(/[&<>"']/g, c => ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c]));
  const formatSize = bytes => bytes < 1024 ? `${bytes || 0} Б` : bytes < 1048576 ? `${(bytes / 1024).toFixed(1)} КБ` : `${(bytes / 1048576).toFixed(1)} МБ`;
  const formatDate = date => new Intl.DateTimeFormat('ru-RU', { day:'numeric', month:'short', year:'numeric' }).format(new Date(date));
  const formatTime = date => new Intl.DateTimeFormat('ru-RU', { hour:'2-digit', minute:'2-digit' }).format(new Date(date));
  const formatDuration = seconds => `${String(Math.floor(seconds / 60)).padStart(2,'0')}:${String(Math.floor(seconds % 60)).padStart(2,'0')}`;
  const recordingTitle = record => escapeHTML(record.title || record.filename.replace(/\.(mp3|wav|m4a|mp4|webm|ogg|opus|aac|flac)$/i, ''));
  const STATUS = {
    completed: ['Готово', 'completed'], processing: ['Обработка', 'processing'],
    uploaded: ['Загружено', 'uploaded'], failed: ['Ошибка', 'failed']
  };
  const state = {
    records: [], stats: { total:0, completed:0, processing:0, failed:0, total_bytes:0 },
    route: 'dashboard', currentId: null, currentRecord: null, result: null,
    search: '', filter: 'all', loading: true, busy: false, serverOnline: true, file: null, drafts:null
  };
  const recorder = { stream:null, media:null, analyser:null, audioCtx:null, animation:null, timer:null,
    chunks:[], blob:null, previewURL:null, startedAt:0, pauseAt:0, pausedMs:0, phase:'idle',
    generation:0, draftId:null, sequence:0, writes:Promise.resolve(), draftError:false,
    name:'', duration:0, stopped:Promise.resolve(), unlock:null, store:null };
  const PROCESSING_STAGES = {queued:'В очереди',transcribing:'Распознавание речи',summarizing:'Создание резюме'};

  function toast(message, isError = false) {
    if (!window.EchoAuth?.authenticated) return;
    const item = document.createElement('div');
    item.className = `toast${isError ? ' error' : ''}`;
    item.innerHTML = `${icon(isError ? 'alert' : 'check')}<span>${escapeHTML(message)}</span>`;
    $('#toasts').append(item);
    setTimeout(() => item.remove(), 5100);
  }

  async function request(path, options = {}) {
    if (!window.EchoAuth?.authenticated) throw new Error('Войдите, чтобы открыть записи.');
    const requestUser = window.EchoAuth.user.id;
    const requestEpoch = accountEpoch;
    let response;
    try {
      response = await fetch(path, { cache:'no-store', ...options, headers:{...options.headers, 'X-Echo-Request':'1', 'X-Echo-Account':requestUser} });
    } catch {
      throw new Error('Не удалось подключиться. Проверьте интернет и попробуйте ещё раз.');
    }
    if (!window.EchoAuth.authenticated || window.EchoAuth.user.id !== requestUser || accountEpoch !== requestEpoch) throw new Error('Войдите, чтобы открыть записи.');
    if (response.headers.get('X-Echo-User') && response.headers.get('X-Echo-User') !== requestUser) { window.EchoAuth.lock(); throw new Error('Аккаунт изменился. Войдите ещё раз.'); }
    if (!response.ok) {
      if (response.status === 401) {
        window.EchoAuth.lock();
        throw new Error('Войдите, чтобы открыть записи.');
      }
      let message = response.status === 413 ? 'Файл слишком большой. Выберите аудио до 200 МБ.' : response.status === 404 ? 'Запись не найдена. Возможно, она была удалена.' : 'Не удалось выполнить действие. Попробуйте ещё раз.';
      throw new Error(message);
    }
    if (response.status === 204) return null;
    const result = await response.json();
    if(!window.EchoAuth.authenticated || window.EchoAuth.user.id!==requestUser || accountEpoch!==requestEpoch)throw new Error('Аккаунт изменился. Войдите ещё раз.');
    return result;
  }

  async function refresh({ silent = false } = {}) {
    if (!window.EchoAuth?.authenticated) return;
    const epoch=accountEpoch;
    try {
      const [list, stats] = await Promise.all([
        request('/api/recordings?limit=100'), request('/api/stats')
      ]);
      if (!window.EchoAuth.authenticated || epoch!==accountEpoch) return;
      const previous = JSON.stringify([state.records.map(r => [r.id,r.updated_at]),state.stats]);
      state.records = list.items || [];
      state.stats = stats;
      state.serverOnline = true;
      state.loading = false;
      updateServer();
      $('#nav-count').textContent = stats.total;
      const next = JSON.stringify([state.records.map(r => [r.id,r.updated_at]),state.stats]);
      if (!silent || previous !== next) {
        if (state.route !== 'detail') render();
      }
      if (state.route === 'detail') await loadDetail(state.currentId, silent);
    } catch (error) {
      if(epoch!==accountEpoch)return;
      state.loading = false;
      state.serverOnline = false;
      updateServer();
      if (!silent) { toast(error.message, true); render(); }
    }
  }

  function updateServer() {
    const indicator = $('#server-indicator');
    indicator.hidden = state.serverOnline;
    indicator.classList.toggle('offline', !state.serverOnline);
    indicator.querySelector('span:last-child').textContent = 'Нет подключения';
  }

  function currentRoute() {
    const path = decodeURIComponent(location.hash.replace(/^#/, '') || '/');
    if (path.startsWith('/recordings/')) return ['detail', path.split('/')[2]];
    if (path.startsWith('/recordings')) return ['recordings'];
    if (path.startsWith('/settings')) return ['settings'];
    return ['dashboard'];
  }

  async function navigate() {
    if (!window.EchoAuth?.authenticated) return;
    const [route, id] = currentRoute();
    state.route = route;
    state.currentId = id || null;
    document.querySelectorAll('[data-nav]').forEach(node => {
      node.classList.toggle('active', node.dataset.nav === (route === 'detail' ? 'recordings' : route));
      if (node.tagName === 'A' && node.classList.contains('active')) node.setAttribute('aria-current', 'page');
      else node.removeAttribute('aria-current');
    });
    const titles = { dashboard:'Обзор', recordings:'Мои записи', detail:'Просмотр записи', settings:'О приложении' };
    $('#crumb-current').textContent = titles[route];
    document.title = `${titles[route]} — Echo`;
    if (route === 'detail') await loadDetail(id);
    else render();
    window.scrollTo({ top:0, behavior:'auto' });
  }

  function statusBadge(status) {
    const [title, cls] = STATUS[status] || ['Неизвестно', 'uploaded'];
    return `<span class="status status-${cls}">${title}</span>`;
  }
  function header(eyebrow, title, description, actions = true) {
    return `<div class="page-head"><div><h1>${title}</h1>${description ? `<p>${description}</p>` : ''}</div>${actions ? `<div class="head-actions"><button class="button button-secondary" data-action="upload">${icon('upload')} Добавить аудио</button><button class="button button-primary record-button" data-action="record">${icon('mic')} Новая запись</button></div>` : ''}</div>`;
  }
  function heroBars() {
    const heights = [12,21,37,26,55,78,36,65,98,48,76,120,88,59,105,52,83,43,69,95,49,74,34,50,27,14,23];
    return `<div class="hero-bars">${heights.map(h => `<i style="height:${h}px"></i>`).join('')}</div>`;
  }
  function emptyState(isSearch = false) {
    if (isSearch) return `<div class="empty-state"><div class="empty-icon">${icon('search')}</div><h3>Ничего не найдено</h3><p>Попробуйте другое название или выберите другой фильтр.</p><button class="button button-secondary" data-action="clear-filters">Сбросить фильтры</button></div>`;
    return `<div class="empty-state"><div class="empty-icon">${icon('mic')}</div><h3>Всё начинается с первой записи</h3><p>Запишите мысль или добавьте аудиофайл.<br>Здесь будут ваши записи и готовые расшифровки.</p></div>`;
  }
  function filteredRecords() {
    return state.records.filter(record => (state.filter === 'all' || record.status === state.filter) && `${record.title || ''} ${record.filename}`.toLowerCase().includes(state.search.toLowerCase()));
  }
  function recordingsTable(records, showFooter = false) {
    if (!records.length) return `<div class="recordings-box">${emptyState(Boolean(state.search || state.filter !== 'all'))}</div>`;
    return `<div class="recordings-box"><div class="table-head"><span>Название</span><span>Дата</span><span>Статус</span><span></span></div>
      ${records.map(record => `<a class="record-row" href="#/recordings/${encodeURIComponent(record.id)}" aria-label="Открыть ${recordingTitle(record)}"><div class="record-name"><span class="record-icon">${icon('headphones')}</span><div style="min-width:0"><strong>${recordingTitle(record)}</strong><small class="record-subdate">${formatDate(record.created_at)}</small></div></div><span class="record-date">${formatDate(record.created_at)}<br><small>${formatTime(record.created_at)}</small></span><span>${statusBadge(record.status)}</span><span class="row-arrow">${icon('arrow')}</span></a>`).join('')}
      ${showFooter ? `<div class="records-bottom"><span>Показано: ${records.length}${state.stats.total > state.records.length ? ` · первые ${state.records.length} из ${state.stats.total}` : ''}</span><button class="text-link" data-action="refresh">Обновить ${icon('refresh')}</button></div>` : ''}</div>`;
  }

  function renderDashboard() {
    const recent = state.records.slice(0,5);
    return `${header('', 'Всё важное — в голосе', 'Идеи, разговоры и немного ясности.', false)}
      <div class="start-grid"><section class="capture-card"><div class="capture-copy"><span class="capture-label">${icon('spark')} МЫСЛИ СТОИТ СОХРАНИТЬ</span><h2>Говорите.<br><em>Echo запомнит.</em></h2><p>Запишите сейчас. Перечитайте главное позже.</p><button class="button button-primary" data-action="record">${icon('mic')} Начать запись</button></div><div class="voice-art" aria-hidden="true">${heroBars()}</div></section>
      <button class="import-card" data-action="upload" type="button"><span class="import-icon">${icon('upload')}</span><span class="import-title">Есть готовая запись?</span><span class="import-description">Добавьте аудио, чтобы получить текст и краткое содержание.</span><span class="import-bottom"><span>Выбрать файл</span>${icon('arrow')}</span></button></div>
      <section><div class="section-head"><div><h2>Последние записи</h2></div><a href="#/recordings" class="text-link">Все записи ${icon('arrow')}</a></div>${recordingsTable(recent)}</section>`;
  }
  function renderLibrary() {
    const chips = [['all','Все'], ['completed','Готовые'], ['uploaded','Не обработаны'], ['processing','В работе'], ['failed','С ошибкой']];
    return `${header('', 'Мои записи', '')}
      <div class="filter-row" role="group" aria-label="Фильтр по статусу">${chips.map(([key, name]) => `<button class="chip ${state.filter === key ? 'active' : ''}" data-action="filter" data-filter="${key}" aria-pressed="${state.filter === key}">${name}</button>`).join('')}</div>
      ${recordingsTable(filteredRecords(),true)}`;
  }
  function renderSettings() {
    return `${header('', 'О приложении', 'Меньше записывать вручную. Больше замечать.', false)}
    <div class="settings-grid"><section class="panel"><div class="panel-head">${icon('spark')}<h3>Ваш голос — в удобном виде</h3></div><ol class="workflow-list"><li><span>01</span><div><strong>Запишите или добавьте аудио</strong><p>Мысль на ходу, разговор или целую встречу.</p></div></li><li><span>02</span><div><strong>Получите текст и главное</strong><p>Откройте запись и нажмите «Получить текст».</p></div></li><li><span>03</span><div><strong>Вернитесь, когда понадобится</strong><p>Слушайте, копируйте текст или скачивайте аудио.</p></div></li></ol></section>
    <section class="panel"><div class="panel-head">${icon('mic')}<h3>Пара простых советов</h3></div><div class="settings-tip"><strong>Для точной расшифровки</strong><p>Говорите ближе к микрофону и по возможности выбирайте тихое место.</p></div><div class="settings-tip"><strong>Если нужно прерваться</strong><p>Поставьте запись на паузу. Незавершённую запись можно найти в черновиках на этом устройстве.</p></div><div class="settings-tip"><strong>Результат можно уточнить</strong><p>Проверьте важные имена, даты и цифры в расшифровке.</p></div></section></div>`;
  }

  const speakerLabel = id => /^speaker_\d+$/.test(id || '') ? `Собеседник ${Number(id.split('_')[1])}` : 'Голос не определён';
  function transcriptForCopy(result) {
    if (!result?.segments?.some(segment => segment.speaker_id)) return result?.transcript || '';
    return result.segments.map(segment => `${speakerLabel(segment.speaker_id)}: ${segment.text}`).join('\n\n');
  }
  function renderTranscript(result) {
    const separated = result.diarization_status === 'completed' && result.segments?.some(segment => segment.speaker_id);
    const hint = ['failed','no_speech'].includes(result.diarization_status) ? '<p class="speaker-hint">Не удалось различить голоса. Текст сохранён — можно попробовать распознать заново.</p>' : '';
    if (!separated) return `${hint}<div class="transcript-text">${escapeHTML(result.transcript)}</div>`;
    return `<div class="speaker-transcript">${result.segments.map(segment => {
      const start = Number(segment.start);
      const color = /^speaker_\d+$/.test(segment.speaker_id || '') ? (Number(segment.speaker_id.split('_')[1])-1)%4 : 4;
      return `<article class="speaker-turn speaker-color-${color}"><div class="speaker-turn-head"><strong>${escapeHTML(speakerLabel(segment.speaker_id))}</strong>${Number.isFinite(start) && start>=0 ? `<button type="button" class="speaker-time" data-action="seek-audio" data-time="${start}" aria-label="Слушать с ${formatDuration(Math.floor(start))}">${formatDuration(Math.floor(start))}</button>` : ''}</div><p>${escapeHTML(segment.text)}</p></article>`;
    }).join('')}</div>`;
  }

  function renderDetail() {
    const r = state.currentRecord;
    if (!r) return `<a class="detail-back" href="#/recordings">${icon('back')} Вернуться к записям</a>${state.loading ? '<div class="loading-state"><div class="spinner"></div>Загрузка записи...</div>' : '<div class="empty-state"><div class="empty-icon">'+icon('file')+'</div><h3>Запись не найдена</h3><p>Проверьте ссылку или вернитесь к своим записям.</p></div>'}`;
    const result = state.result;
    const processing = r.status === 'processing';
    const transcript = result?.transcript;
    const summary = result?.summary;
    const stage = PROCESSING_STAGES[r.processing_stage] || 'Обработка аудио';
    return `<a class="detail-back" href="#/recordings">${icon('back')} Все записи</a>
      <div class="page-head detail-head"><div><h1>${recordingTitle(r)}</h1><p>${formatDate(r.created_at)} · ${formatTime(r.created_at)}${processing ? ` · ${stage}` : ''}</p></div><div class="head-actions">${statusBadge(r.status)}<a class="button button-secondary" href="/api/recordings/${encodeURIComponent(r.id)}/audio?download=1" download>${icon('download')} Скачать</a></div></div>
      <div class="detail-grid"><div class="detail-main"><section class="panel"><div class="panel-head">${icon('headphones')}<h3>Аудиозапись</h3></div><audio class="audio-player" controls preload="metadata" src="/api/recordings/${encodeURIComponent(r.id)}/audio">Ваш браузер не поддерживает воспроизведение аудио.</audio><details class="audio-help"><summary>Не воспроизводится?</summary><p>Скачайте оригинал и откройте в аудиоплеере на устройстве.</p></details></section>
      <section class="panel"><div class="panel-head">${icon('file')}<h3>Расшифровка</h3>${transcript ? `<button class="text-link" style="margin-left:auto" data-action="copy-transcript">${icon('copy')} Копировать</button>` : ''}</div>${transcript ? `${renderTranscript(result)}` : processing ? `<div class="pending-pane"><span class="waiting-icon"><span class="spinner"></span></span><div><strong>Идёт распознавание...</strong><br>Можно вернуться позже — результат появится здесь.</div></div><div class="placeholder-lines"><i></i><i></i><i></i></div>` : `<div class="pending-pane"><span class="waiting-icon">${icon('mic')}</span><div>Нажмите «Получить текст», чтобы прочитать запись.</div></div>`}</section>
      </div><aside class="detail-side"><section class="panel"><div class="panel-head">${icon('spark')}<h3>Главное</h3></div>${summary ? `<p class="panel-desc" style="white-space:pre-wrap">${escapeHTML(result.summary)}</p>` : `<p class="panel-desc">${processing ? (transcript ? 'Собираем главные мысли из записи…' : 'Сначала подготовим текст, затем выделим главное.') : 'Краткое содержание и главные мысли появятся здесь после обработки.'}</p>`}
      ${result?.key_points?.length ? `<hr style="border:0;border-top:1px solid var(--line);margin:18px 0"><h4 style="font-size:11px;margin:0 0 13px">Ключевые мысли</h4><ul class="key-points">${result.key_points.map(point => `<li>${escapeHTML(point)}</li>`).join('')}</ul>` : ''}</section>
      <section class="panel"><div class="panel-head">${icon('settings')}<h3>Ваша запись</h3></div><div class="action-stack"><button class="button button-primary" data-action="process" ${processing || state.busy ? 'disabled' : ''}>${processing ? `<span class="spinner"></span> ${stage}` : `${icon('spark')} ${transcript ? 'Распознать заново' : 'Получить текст'}`}</button>${transcript ? `<button class="button button-secondary" data-action="summarize" ${processing || state.busy ? 'disabled' : ''}>${icon('spark')} ${summary ? 'Обновить резюме' : 'Создать резюме'}</button>` : ''}<button class="button button-secondary" data-action="refresh-detail">${icon('refresh')} Обновить статус</button><button class="button button-danger" data-action="delete">${icon('x')} Удалить запись</button></div>${r.status === 'failed' ? `<div class="error-box" style="margin-top:14px">${r.processing_error === 'Whisper вернул пустую расшифровку' ? 'Не удалось разобрать речь. Попробуйте распознать запись ещё раз. Если это не поможет, сделайте новую запись ближе к микрофону.' : 'Не удалось завершить обработку. Попробуйте ещё раз.'}${transcript ? '<br>Расшифровка сохранена. Можно отдельно повторить создание резюме.' : ''}</div>` : ''}</section></aside></div>`;
  }

  function render() {
    if (!window.EchoAuth?.authenticated) return;
    const root = $('#view');
    if (state.loading) { root.innerHTML = '<div class="loading-state"><div class="spinner"></div>Загружаем ваше пространство...</div>'; return; }
    if (state.route === 'dashboard') root.innerHTML = renderDashboard();
    else if (state.route === 'recordings') root.innerHTML = renderLibrary();
    else if (state.route === 'settings') root.innerHTML = renderSettings();
    else if (state.route === 'detail') root.innerHTML = renderDetail();
  }

  async function loadDetail(id, silent = false) {
    const epoch=accountEpoch;
    if (!id || !/^[0-9a-f-]{36}$/i.test(id)) { state.currentRecord = null; state.result = null; render(); return; }
    try {
      const r = await request(`/api/recordings/${encodeURIComponent(id)}`);
      if (!window.EchoAuth.authenticated || epoch!==accountEpoch || state.route !== 'detail' || state.currentId !== id) return;
      const unchanged = silent && state.result && state.currentRecord?.updated_at === r.updated_at && state.currentRecord?.id === r.id;
      if (unchanged) return;
      const result = await request(`/api/recordings/${encodeURIComponent(id)}/result`);
      if (!window.EchoAuth.authenticated || epoch!==accountEpoch || state.route !== 'detail' || state.currentId !== id) return;
      state.currentRecord = result.recording;
      state.result = result;
      render();
    } catch (error) {
      if (epoch!==accountEpoch || state.route !== 'detail' || state.currentId !== id) return;
      if (!silent) toast(error.message, true);
      state.currentRecord = null;
      state.result = null;
      render();
    }
  }

  function openUpload() {
    state.file = null;
    $('#file-input').value = '';
    $('#selected-file').hidden = true;
    $('#upload-submit').disabled = true;
    $('#upload-modal').showModal();
  }
  function chooseFile(file) {
    if (!file) return;
    if (file.size > 200 * 1024 * 1024) { toast('Файл превышает 200 МБ', true); return; }
    if (file.size === 0) { toast('Файл пустой', true); return; }
    if (!(file.type.startsWith('audio/') || /\.(mp3|wav|m4a|mp4|webm|ogg|opus|aac|flac)$/i.test(file.name))) {
      toast('Выберите аудиофайл поддерживаемого формата', true); return;
    }
    state.file = file;
    const selected = $('#selected-file');
    selected.innerHTML = `${icon('file')}<span>${escapeHTML(file.name)}</span><small>${formatSize(file.size)}</small>`;
    selected.hidden = false;
    $('#upload-submit').disabled = false;
  }
  async function uploadFile(file) {
    if (!file || state.busy) return;
    const epoch=accountEpoch;
    state.busy = true;
    const uploadBtn = $('#upload-submit');
    uploadBtn.disabled = true;
    uploadBtn.innerHTML = '<span class="spinner"></span> Загружаем...';
    try {
      const form = new FormData();
      form.append('audio', file, file.name);
      const created = await request('/api/recordings', { method:'POST', body:form });
      $('#upload-modal').close();
      toast('Запись добавлена');
      await refresh({ silent:true });
      if(epoch!==accountEpoch)return;
      location.hash = `#/recordings/${created.id}`;
      if (state.currentId === created.id) await loadDetail(created.id);
    } catch (error) {
      toast(error.message, true);
    } finally {
      if(epoch!==accountEpoch)return;
      state.busy = false;
      uploadBtn.disabled = !state.file;
      uploadBtn.innerHTML = `Загрузить файл ${icon('arrow')}`;
      if (state.route === 'detail') render();
    }
  }

  async function refreshDraftNotice() {
    if (!window.EchoAuth?.authenticated) return;
    try {
      const store = state.drafts;
      if (!store) return;
      const drafts = await store.list();
      if (state.drafts !== store || !window.EchoAuth.authenticated) return;
      const pending = drafts.filter(draft => draft.id !== recorder.draftId);
      const notice = $('#draft-notice');
      notice.hidden = !pending.length;
      notice.innerHTML = pending.length ? `<strong>Несохранённые записи</strong><p class="panel-desc">Продолжите с того места, где остановились. Если запись прервалась, проверьте её перед сохранением.</p><div class="action-stack">${pending.map(draft => `<button class="button button-secondary" data-action="restore-draft" data-draft-id="${escapeHTML(draft.id)}">${icon('mic')} ${escapeHTML(draft.name || 'Черновик')} · ${formatSize(draft.bytes)}</button>`).join('')}</div>` : '';
    } catch { /* Recording reports storage failures explicitly when it starts. */ }
  }
  function draftMetadata() {
    return {name:recorder.name, mimeType:recorder.media?.mimeType || recorder.blob?.type || '', duration:elapsedSeconds()};
  }
  function draftFailure(error) {
    if (!recorder.draftError) toast('Не удалось сохранить черновик. Скачайте аудио перед закрытием страницы.',true);
    recorder.draftError = true;
  }
  function queueDraftUpdate(metadata) {
    const id = recorder.draftId;
    const store = recorder.store;
    if (!id || recorder.draftError || !store) return;
    recorder.writes = recorder.writes.then(() => store.update(id,metadata)).catch(draftFailure);
  }
  async function restoreDraft(id) {
    if (recorder.phase !== 'idle' || state.busy) { toast('Сначала завершите текущую запись.',true); return; }
    state.busy = true;
    let unlock;
    const store=state.drafts, epoch=accountEpoch;
    try {
      unlock = await store.claim(id);
      if (!unlock) throw new Error('Этот черновик открыт в другой вкладке.');
      const draft = await store.load(id);
      if(epoch!==accountEpoch)return;
      if (!draft.blob.size || draft.uploaded) throw new Error('Эта запись уже сохранена или не содержит звука');
      clearRecorder();
      Object.assign(recorder,{store,draftId:id,unlock,blob:draft.blob,previewURL:URL.createObjectURL(draft.blob),
        name:draft.name || 'Восстановленная запись',duration:draft.duration || 0,phase:'ready'});
      unlock = null;
      $('#record-modal').showModal();
      drawRecorderUI();
      await refreshDraftNotice();
    } catch(error) { toast('Не удалось открыть или обновить черновик. Попробуйте ещё раз.',true); }
    finally {
      unlock?.();
      if(epoch!==accountEpoch)return;
      state.busy=false;
      if ($('#record-modal').open && recorder.phase === 'ready') drawRecorderUI();
    }
  }
  function clearRecorder() {
    if (recorder.timer) clearInterval(recorder.timer);
    if (recorder.animation) cancelAnimationFrame(recorder.animation);
    if (recorder.stream) recorder.stream.getTracks().forEach(track => track.stop());
    if (recorder.audioCtx) recorder.audioCtx.close().catch(() => {});
    if (recorder.previewURL) URL.revokeObjectURL(recorder.previewURL);
    recorder.unlock?.();
    const generation = recorder.generation + 1;
    Object.assign(recorder, { stream:null, media:null, analyser:null, audioCtx:null, animation:null, timer:null,
      chunks:[], blob:null, previewURL:null, startedAt:0, pauseAt:0, pausedMs:0, phase:'idle',generation,
      draftId:null,sequence:0,writes:Promise.resolve(),draftError:false,name:'',duration:0,stopped:Promise.resolve(),unlock:null,store:null });
  }
  function openRecord() {
    if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) {
      toast('Микрофон недоступен. Откройте приложение по защищённой ссылке в современном браузере или добавьте готовое аудио.', true);
      return;
    }
    clearRecorder();
    $('#record-modal').showModal();
    drawRecorderUI();
  }
  async function closeRecord() {
    if (state.busy) return;
    const epoch=accountEpoch, generation=recorder.generation;
    if (['recording','paused','stopping'].includes(recorder.phase)) await stopRecording();
    await recorder.writes;
    if(epoch!==accountEpoch || generation!==recorder.generation)return;
    if (recorder.draftError && recorder.blob && !confirm('Черновик не удалось сохранить. Закрыть окно и потерять аудио?')) return;
    clearRecorder();
    $('#record-modal').close();
    await refreshDraftNotice();
  }
  async function discardRecording() {
    if (state.busy || recorder.phase !== 'ready') return;
    if (!confirm('Удалить этот черновик и начать новую запись?')) return;
    const epoch=accountEpoch, generation=recorder.generation, store=recorder.store, id=recorder.draftId;
    state.busy = true;
    try {
      await recorder.writes;
      if (id) await store.remove(id);
      if(epoch!==accountEpoch || generation!==recorder.generation)return;
      clearRecorder();drawRecorderUI();await refreshDraftNotice();
    } catch(error) { toast('Не удалось открыть или обновить черновик. Попробуйте ещё раз.',true); }
    finally { if(epoch===accountEpoch){state.busy=false; if ($('#record-modal').open) drawRecorderUI();} }
  }
  function drawRecorderUI() {
    const body = $('#recorder-body');
    const phase = recorder.phase;
    const timer = formatDuration(elapsedSeconds());
    if (phase === 'ready' && !recorder.name) recorder.name = 'Новая запись';
    body.innerHTML = `<div class="recorder-center ${phase === 'idle' ? 'recorder-ready' : phase === 'ready' ? 'recorder-preview-ready' : ''}">
      <div class="recorder-canvas-wrap"><canvas id="wave-canvas" width="430" height="165" aria-label="Индикатор громкости"></canvas></div>
      <div class="record-timer" id="record-timer">${timer}</div>
      <div class="record-state">${phase === 'recording' ? '<span class="record-indicator">ИДЁТ ЗАПИСЬ</span>' : phase === 'paused' ? 'Запись на паузе' : phase === 'ready' ? 'Ваша запись готова.' : phase === 'starting' ? 'Подготовка микрофона...' : phase === 'stopping' ? 'Сохраняем запись…' : 'Всё готово. Можно начинать.'}</div>
      ${phase === 'ready' ? `<audio id="record-preview" class="record-preview" controls src="${recorder.previewURL}"></audio><label class="field-label" for="record-name">Название до обработки</label><input id="record-name" class="record-name-input" maxlength="120" value="${escapeHTML(recorder.name)}" /><p class="panel-desc">После обработки название появится по содержанию. ${recorder.draftError ? 'Черновик не сохранён на устройстве. Скачайте аудио перед закрытием.' : 'Черновик сохранён на этом устройстве.'}</p>` : ''}
      <div class="recorder-controls">
      ${phase === 'idle' ? `<button class="button button-primary" data-action="start-record">${icon('mic')} Начать запись</button>` : ''}
      ${phase === 'recording' ? `<button class="button button-secondary" data-action="pause-record">${icon('pause')} Пауза</button><button class="button button-primary" data-action="stop-record">${icon('stop')} Завершить</button>` : ''}
      ${phase === 'paused' ? `<button class="button button-secondary" data-action="resume-record">${icon('play')} Продолжить</button><button class="button button-primary" data-action="stop-record">${icon('stop')} Завершить</button>` : ''}
      ${phase === 'ready' ? `<button class="button button-secondary" data-action="reset-record">${icon('refresh')} Записать заново</button><button class="button button-secondary" data-action="download-record">${icon('download')} Скачать аудио</button><button class="button button-primary" data-action="save-record" ${state.busy ? 'disabled' : ''}>${icon('upload')} Сохранить запись</button>` : ''}
      </div></div>`;
    if (phase === 'recording' || phase === 'paused') visualizer();
    else drawStillWave();
  }
  function elapsedSeconds() {
    if (recorder.phase === 'ready') return recorder.duration;
    if (!recorder.startedAt) return 0;
    const now = recorder.pauseAt || Date.now();
    return Math.max(0, (now - recorder.startedAt - recorder.pausedMs) / 1000);
  }
  function drawStillWave() {
    const canvas = $('#wave-canvas');
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    const w = canvas.width, h = canvas.height;
    ctx.clearRect(0,0,w,h);
    for (let i=0;i<55;i++) {
      const x = 16+i*7.3;
      const height = (6 + Math.abs(Math.sin(i*1.2)*Math.cos(i*.32))*45);
      ctx.fillStyle = '#a18be6';
      ctx.beginPath(); ctx.roundRect(x,h/2-height/2,3.6,height,2); ctx.fill();
    }
  }
  function visualizer() {
    if (recorder.animation) cancelAnimationFrame(recorder.animation);
    const canvas = $('#wave-canvas');
    if (!canvas || !recorder.analyser) { drawStillWave(); return; }
    const ctx = canvas.getContext('2d');
    const data = new Uint8Array(recorder.analyser.frequencyBinCount);
    const frame = () => {
      if (!$('#wave-canvas') || recorder.phase === 'idle' || recorder.phase === 'ready') return;
      recorder.analyser.getByteFrequencyData(data);
      ctx.clearRect(0,0,canvas.width,canvas.height);
      for (let i=0;i<56;i++) {
        const bin = Math.min(data.length-1,Math.floor(i / 56 * data.length * .7));
        const amplitude = recorder.phase === 'paused' ? 3 : Math.max(4,Math.min(126,data[bin]*.7));
        const x = 11+i*7.3;
        ctx.fillStyle = i%5 === 0 ? '#6653e4' : '#b19bed';
        ctx.beginPath();ctx.roundRect(x,canvas.height/2-amplitude/2,3.5,amplitude,2);ctx.fill();
      }
      recorder.animation = requestAnimationFrame(frame);
    };
    frame();
  }
  async function startRecording() {
    if (recorder.phase !== 'idle' || state.busy) return;
    const generation = recorder.generation;
    const store = state.drafts;
    recorder.store = store;
    recorder.phase = 'starting';
    drawRecorderUI();
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio:{ echoCancellation:true, noiseSuppression:true } });
      if (recorder.generation !== generation) { stream.getTracks().forEach(track=>track.stop()); return; }
      recorder.stream = stream;
      const candidates = ['audio/webm;codecs=opus','audio/mp4','audio/webm','audio/ogg;codecs=opus'];
      const type = candidates.find(candidate => MediaRecorder.isTypeSupported(candidate)) || '';
      recorder.media = new MediaRecorder(recorder.stream, type ? { mimeType:type } : {});
      recorder.chunks = [];
      recorder.name = `Запись ${new Date().toLocaleString('ru-RU')}`;
      const draftId = crypto.randomUUID();
      try {
        const unlock = await store.claim(draftId);
        if (recorder.generation !== generation) { unlock?.(); return; }
        recorder.unlock = unlock;
        if (!unlock) throw new Error('Черновик занят другой вкладкой');
        recorder.draftId = draftId;
        await store.create(draftId, {name:recorder.name,mimeType:recorder.media.mimeType,duration:0});
      } catch(error) {
        if (recorder.generation === generation) draftFailure(error);
      }
      if (recorder.generation !== generation) return;
      let resolveStopped;
      recorder.stopped = new Promise(resolve => { resolveStopped = resolve; });
      recorder.media.ondataavailable = event => {
        if (!event.data.size) return;
        recorder.chunks.push(event.data);
        if (recorder.draftError) return;
        const sequence = recorder.sequence++;
        const metadata = draftMetadata();
        recorder.writes = recorder.writes.then(() => {
          if (!recorder.draftError) return store.append(draftId, sequence, event.data, metadata);
        }).catch(draftFailure);
      };
      recorder.media.onstop = async () => {
        recorder.blob = new Blob(recorder.chunks,{ type:recorder.media.mimeType || 'audio/webm' });
        recorder.duration = elapsedSeconds();
        recorder.previewURL = URL.createObjectURL(recorder.blob);
        recorder.stream?.getTracks().forEach(track=>track.stop());
        recorder.audioCtx?.close().catch(()=>{});
        recorder.audioCtx=null;
        clearInterval(recorder.timer);
        cancelAnimationFrame(recorder.animation);
        queueDraftUpdate({...draftMetadata(),duration:recorder.duration});
        await recorder.writes;
        recorder.phase = 'ready';
        drawRecorderUI();resolveStopped();
      };
      recorder.media.onerror = () => toast('Ошибка записи с микрофона', true);
      try {
        const AudioContextClass = window.AudioContext || window.webkitAudioContext;
        recorder.audioCtx = new AudioContextClass();
        const source = recorder.audioCtx.createMediaStreamSource(recorder.stream);
        recorder.analyser = recorder.audioCtx.createAnalyser();
        recorder.analyser.fftSize = 256;
        source.connect(recorder.analyser);
      } catch { recorder.analyser = null; }
      recorder.startedAt = Date.now();
      recorder.pausedMs = 0;
      recorder.pauseAt = 0;
      recorder.phase = 'recording';
      recorder.media.start(500);
      drawRecorderUI();
      recorder.timer = setInterval(() => { const timer = $('#record-timer'); if(timer) timer.textContent = formatDuration(elapsedSeconds()); }, 250);
    } catch (error) {
      if (recorder.generation !== generation) return;
      recorder.stream?.getTracks().forEach(track=>track.stop());
      recorder.audioCtx?.close().catch(()=>{});
      recorder.audioCtx=null;
      recorder.unlock?.();recorder.unlock=null;
      recorder.phase='idle';
      toast(error.name === 'NotAllowedError' ? 'Разрешите доступ к микрофону в настройках браузера.' : 'Не удалось начать запись. Проверьте микрофон и попробуйте ещё раз.', true);
      drawRecorderUI();
    }
  }
  function pauseRecording() {
    if (recorder.media?.state !== 'recording') return;
    recorder.media.pause(); recorder.pauseAt = Date.now(); recorder.phase='paused'; drawRecorderUI();
  }
  function resumeRecording() {
    if (recorder.media?.state !== 'paused') return;
    recorder.media.resume();recorder.pausedMs += Date.now() - recorder.pauseAt;recorder.pauseAt = 0;recorder.phase='recording';drawRecorderUI();
  }
  function stopRecording() {
    if (recorder.phase === 'stopping') return recorder.stopped;
    if (!recorder.media || recorder.media.state === 'inactive') return recorder.stopped;
    recorder.phase = 'stopping';
    recorder.media.stop();
    drawRecorderUI();
    return recorder.stopped;
  }
  function recordingFilename() {
    const name = (recorder.name.trim() || 'Новая запись').replace(/[\\/:*?"<>|]/g, '_');
    const mime = recorder.blob.type;
    return name + (mime.includes('mp4') ? '.m4a' : mime.includes('ogg') ? '.ogg' : '.webm');
  }
  function downloadRecording() {
    if (!recorder.blob) return;
    const link = document.createElement('a');
    link.href=recorder.previewURL;link.download=recordingFilename();link.click();
  }
  async function saveRecording() {
    if (!recorder.blob || state.busy) return;
    const epoch=accountEpoch, store=recorder.store, draftId=recorder.draftId, writes=recorder.writes, generation=recorder.generation;
    const mime = recorder.blob.type;
    const file = new File([recorder.blob], recordingFilename(), {type:mime});
    state.busy = true;
    const save = document.querySelector('[data-action="save-record"]');
    if (save) { save.disabled=true;save.innerHTML='<span class="spinner"></span> Сохраняем...'; }
    try {
      const data = new FormData();data.append('audio',file,file.name);
      const created = await request('/api/recordings', {method:'POST',body:data});
      await writes;
      if (draftId) {
        try {
          await store.update(draftId,{uploaded:created.id});
          await store.remove(draftId);
        } catch { toast('Запись сохранена. Её черновик пока остался на устройстве.',true); }
      }
      if(epoch!==accountEpoch || generation!==recorder.generation)return;
      $('#record-modal').close();
      clearRecorder();
      await refreshDraftNotice();
      toast('Готово! Аудиозапись сохранена.');
      await refresh({silent:true});
      if(epoch!==accountEpoch)return;
      location.hash = `#/recordings/${created.id}`;
      if (state.currentId === created.id) await loadDetail(created.id);
    } catch (error) { toast(error.message,true); }
    finally {
      if(epoch!==accountEpoch)return;
      state.busy=false;
      if ($('#record-modal').open && recorder.phase === 'ready') drawRecorderUI();
      else if (state.route === 'detail') render();
    }
  }

  async function processRecording(mode = 'full') {
    if (!state.currentId || state.busy) return;
    const epoch=accountEpoch;
    const id = state.currentId;
    state.busy=true;
    render();
    try {
      await request(`/api/recordings/${encodeURIComponent(id)}/process?mode=${mode}`, {method:'POST'});
      toast(mode === 'summary' ? 'Готовим краткое содержание.' : 'Готовим текст записи.');
      await loadDetail(id);
    } catch (error) { toast(error.message,true); }
    finally {if(epoch===accountEpoch){state.busy=false; render();}}
  }
  async function deleteRecording() {
    const epoch=accountEpoch;
    const id = state.currentId;
    if (!id || state.busy) return;
    if (!confirm('Удалить запись и результаты анализа без возможности восстановления?')) return;
    state.busy=true;
    try {
      await request(`/api/recordings/${encodeURIComponent(id)}`, {method:'DELETE'});
      toast('Запись удалена');
      state.currentRecord=null;state.result=null;
      location.hash='#/recordings';
      await refresh({silent:true});
    } catch (error) {toast(error.message,true);}
    finally {if(epoch===accountEpoch)state.busy=false;}
  }

  async function handleAction(event) {
    const button = event.target.closest('[data-action]');
    if (!button || button.disabled) return;
    const action=button.dataset.action;
    if (['record','upload','start-record','pause-record','resume-record','stop-record','reset-record','save-record','close-record','close-upload','submit-upload','process','delete','copy-transcript','refresh','refresh-detail','filter','clear-filters'].includes(action)) event.preventDefault();
    switch (action) {
      case 'logout':
        try {
          if ($('#record-modal').open) await closeRecord();
          await window.EchoAuth.logout();
        } catch (error) { toast(error.message,true); }
        break;
      case 'record': if (!state.busy) openRecord();break;
      case 'upload': openUpload();break;
      case 'close-record': await closeRecord();break;
      case 'close-upload': $('#upload-modal').close();break;
      case 'submit-upload': await uploadFile(state.file);break;
      case 'start-record': await startRecording();break;
      case 'pause-record': pauseRecording();break;
      case 'resume-record': resumeRecording();break;
      case 'stop-record': await stopRecording();break;
      case 'reset-record': await discardRecording();break;
      case 'download-record': downloadRecording();break;
      case 'restore-draft': await restoreDraft(button.dataset.draftId);break;
      case 'save-record': await saveRecording();break;
      case 'process':await processRecording();break;
      case 'summarize':await processRecording('summary');break;
      case 'delete':await deleteRecording();break;
      case 'refresh':await refresh();toast('Данные обновлены');break;
      case 'refresh-detail':await loadDetail(state.currentId);toast('Статус обновлён');break;
      case 'filter':state.filter=button.dataset.filter;render();break;
      case 'clear-filters':state.filter='all';state.search='';$('#search-input').value='';render();break;
      case 'seek-audio': {
        const player = document.querySelector('.audio-player');
        const seconds = Number(button.dataset.time);
        if (player && Number.isFinite(seconds) && seconds>=0) {
          if (player.readyState>0) player.currentTime=seconds;
          try { await player.play(); player.currentTime=seconds; } catch { toast('Нажмите воспроизведение, чтобы прослушать реплику.'); }
        }
        break;
      }
      case 'copy-transcript': {
        try {await navigator.clipboard.writeText(transcriptForCopy(state.result));toast('Текст скопирован');}
        catch {toast('Не удалось скопировать текст в буфер обмена',true);} break;
      }
    }
  }

  function init() {
    document.addEventListener('click', handleAction);
    document.addEventListener('input',event => {
      if (event.target.id === 'record-name') {
        recorder.name = event.target.value;
        queueDraftUpdate({name:recorder.name});
      }
    });
    window.addEventListener('beforeunload', event => {
      if (recorder.phase !== 'idle') { event.preventDefault(); event.returnValue=''; }
    });
    window.addEventListener('focus', refreshDraftNotice);
    window.addEventListener('hashchange', navigate);
    $('#search-input').addEventListener('input', event => {
      state.search = event.target.value;
      if (state.route !== 'recordings') location.hash = '#/recordings';
      else render();
    });
    document.addEventListener('keydown', event => {
      if (event.key === '/' && !['INPUT','TEXTAREA'].includes(document.activeElement.tagName) && !$('#record-modal').open && !$('#upload-modal').open) {
        event.preventDefault();$('#search-input').focus();
      }
    });
    $('#file-input').addEventListener('change', event => chooseFile(event.target.files?.[0]));
    const dropzone=$('#dropzone');
    dropzone.addEventListener('click', () => $('#file-input').click());
    dropzone.addEventListener('keydown',event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();$('#file-input').click();}});
    dropzone.addEventListener('dragover',event=>{event.preventDefault();dropzone.classList.add('dragging');});
    dropzone.addEventListener('dragleave',()=>dropzone.classList.remove('dragging'));
    dropzone.addEventListener('drop',event=>{event.preventDefault();dropzone.classList.remove('dragging');chooseFile(event.dataTransfer?.files?.[0]);});
    $('#record-modal').addEventListener('cancel', event => {event.preventDefault();closeRecord();});
    $('#upload-modal').addEventListener('close',()=>{state.file=null;});
    navigate();
    refresh();
    refreshDraftNotice();
    setInterval(()=>{if(window.EchoAuth.authenticated&&document.visibilityState==='visible'&&!state.busy) refresh({silent:true});},5000);
    if ('serviceWorker' in navigator && window.isSecureContext) {
      window.addEventListener('load',()=>navigator.serviceWorker.register('/sw.js').catch(()=>{}));
    }
  }
  let initialized = false;
  let accountEpoch = 0;
  let lockedCleanup = Promise.resolve();
  async function beginSession() {
    const epoch=++accountEpoch,user=window.EchoAuth.user;
    await lockedCleanup;
    if(epoch!==accountEpoch||!user||window.EchoAuth.user?.id!==user.id)return;
    const store=window.EchoDrafts.forUser(user.id);
    state.drafts=store;
    if(user.legacyOwner)try{await store.importLegacy();}catch{toast('Не удалось перенести старые черновики. Они сохранены на этом устройстве.',true);}
    if(epoch!==accountEpoch||!window.EchoAuth.authenticated)return;
    if(!initialized){initialized=true;init();}
    else{navigate();refresh();refreshDraftNotice();}
  }
  window.addEventListener('echo:login',beginSession);
  window.addEventListener('echo:locked', () => {
    accountEpoch++;
    lockedCleanup=(async()=>{
    document.querySelectorAll('audio').forEach(audio=>{audio.pause();audio.removeAttribute('src');audio.load();});
    document.querySelectorAll('dialog[open]').forEach(dialog => dialog.close());
    $('#view').replaceChildren();
    $('#toasts').replaceChildren();
    $('#draft-notice').hidden = true;
    $('#draft-notice').replaceChildren();
    $('#nav-count').textContent = '0';
    state.records = []; state.currentRecord = null; state.result = null; state.currentId=null; state.loading=true; state.busy=false;
    state.search = ''; state.filter = 'all'; $('#search-input').value = '';
    state.stats = {total:0,completed:0,processing:0,failed:0,total_bytes:0};
    if (['recording','paused','stopping'].includes(recorder.phase)) await stopRecording();
    await recorder.writes;
    clearRecorder();
    $('#recorder-body').replaceChildren();
    state.file = null; $('#file-input').value = ''; $('#selected-file').replaceChildren();
    state.drafts=null;
    document.title = 'Вход — Echo';
    })();
  });
  if(window.EchoAuth?.authenticated)beginSession();
})();
