// privatedns dashboard — vanilla SPA. Talks to /api/v1 via the reverse proxy.

const API = '/api/v1';
const state = { token: null, user: null, currentZone: null };

// ---- fetch wrapper -----------------------------------------------------
async function api(path, opts = {}) {
  const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
  if (state.token) headers.Authorization = `Bearer ${state.token}`;
  const res = await fetch(API + path, { ...opts, headers });
  const text = await res.text();
  const body = text ? JSON.parse(text) : null;
  if (!res.ok) throw Object.assign(new Error(body?.error || res.statusText), { status: res.status, body });
  return body;
}

function toast(msg, isErr = false) {
  const t = document.getElementById('toast');
  if (!t) return;
  t.textContent = msg;
  t.className = isErr ? 'error' : '';
  t.hidden = false;
  clearTimeout(toast._h);
  toast._h = setTimeout(() => (t.hidden = true), 4000);
}

// ---- persist auth ------------------------------------------------------
function loadAuth() {
  try {
    const s = JSON.parse(localStorage.getItem('privatedns.auth') || 'null');
    if (s?.token) { state.token = s.token; state.user = s.user; }
  } catch { /* ignore */ }
}
function saveAuth() { localStorage.setItem('privatedns.auth', JSON.stringify({ token: state.token, user: state.user })); }
function clearAuth() { localStorage.removeItem('privatedns.auth'); state.token = null; state.user = null; }

// ---- rendering ---------------------------------------------------------
function tpl(id) { return document.getElementById(id).content.cloneNode(true); }
function mount(node) { const app = document.getElementById('app'); app.innerHTML = ''; app.appendChild(node); }

// ---- login -------------------------------------------------------------
function renderLogin() {
  const node = tpl('tpl-login');
  const form = node.querySelector('#login-form');
  const err = node.querySelector('[data-role=err]');
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    err.hidden = true;
    const fd = new FormData(form);
    try {
      const r = await api('/auth/login', { method: 'POST', body: JSON.stringify({ email: fd.get('email'), password: fd.get('password') }) });
      state.token = r.token; state.user = r.user; saveAuth();
      renderShell();
    } catch (e) { err.hidden = false; err.textContent = e.message || 'login failed'; }
  });
  mount(node);
}

// ---- shell -------------------------------------------------------------
function renderShell() {
  const node = tpl('tpl-shell');
  node.querySelector('[data-role=user-email]').textContent = state.user?.email || '';
  const buttons = node.querySelectorAll('nav button');
  buttons.forEach((b) => b.addEventListener('click', () => go(b.dataset.nav, buttons)));
  node.querySelector('#logout').addEventListener('click', async () => {
    try { await api('/auth/logout', { method: 'POST' }); } catch {}
    clearAuth(); renderLogin();
  });
  mount(node);
  buttons[0].click();
}

function go(name, buttons) {
  buttons.forEach((b) => b.classList.toggle('active', b.dataset.nav === name));
  const map = { zones: renderZones, records: renderRecords, keys: renderKeys, users: renderUsers, audit: renderAudit, health: renderHealth };
  const view = document.getElementById('view');
  view.innerHTML = '';
  map[name]?.(view);
}

// ---- zones -------------------------------------------------------------
async function renderZones(view) {
  view.appendChild(tpl('tpl-zones'));
  const rows = view.querySelector('[data-role=rows]');
  const form = view.querySelector('#create-zone');
  const load = async () => {
    rows.innerHTML = '';
    const zs = await api('/zones');
    zs.sort((a, b) => a.name.localeCompare(b.name));
    for (const z of zs) {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td>${z.name}</td><td>${z.is_private ? 'yes' : 'no'}</td><td>${z.serial || ''}</td><td>${z.description || ''}</td><td class="actions"></td>`;
      const del = document.createElement('button');
      del.textContent = 'delete'; del.className = 'danger';
      del.addEventListener('click', async () => {
        if (!confirm(`Delete zone ${z.name}? This removes all records.`)) return;
        try { await api('/zones/' + encodeURIComponent(z.name), { method: 'DELETE' }); toast('deleted'); await load(); }
        catch (e) { toast(e.message, true); }
      });
      tr.querySelector('.actions').appendChild(del);
      rows.appendChild(tr);
    }
  };
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    try {
      await api('/zones', { method: 'POST', body: JSON.stringify({ name: fd.get('name'), description: fd.get('description') }) });
      form.reset(); toast('zone created'); await load();
    } catch (e) { toast(e.message, true); }
  });
  await load();
}

// ---- records -----------------------------------------------------------
async function renderRecords(view) {
  view.appendChild(tpl('tpl-records'));
  const select = view.querySelector('[data-role=zone-select]');
  const rows = view.querySelector('[data-role=rows]');
  const form = view.querySelector('#upsert-record');

  const zs = await api('/zones');
  zs.sort((a, b) => a.name.localeCompare(b.name));
  select.innerHTML = '';
  for (const z of zs) {
    const opt = document.createElement('option'); opt.value = z.name; opt.textContent = z.name;
    select.appendChild(opt);
  }
  if (state.currentZone && zs.find((z) => z.name === state.currentZone)) select.value = state.currentZone;
  else state.currentZone = select.value;
  select.addEventListener('change', () => { state.currentZone = select.value; loadRows(); });

  async function loadRows() {
    rows.innerHTML = '';
    if (!state.currentZone) return;
    const rs = await api('/zones/' + encodeURIComponent(state.currentZone) + '/records');
    for (const set of rs) {
      for (const r of set.records) {
        const tr = document.createElement('tr');
        tr.innerHTML = `<td>${r.name}</td><td>${r.type}</td><td>${r.ttl}</td><td>${escapeHtml(r.content)}</td><td class="actions"></td>`;
        const del = document.createElement('button'); del.textContent = 'delete'; del.className = 'danger';
        del.addEventListener('click', async () => {
          if (!confirm(`Delete ${r.name} ${r.type}?`)) return;
          try {
            await api('/zones/' + encodeURIComponent(state.currentZone) + '/records?name=' + encodeURIComponent(r.name) + '&type=' + encodeURIComponent(r.type), { method: 'DELETE' });
            toast('deleted'); await loadRows();
          } catch (e) { toast(e.message, true); }
        });
        tr.querySelector('.actions').appendChild(del);
        rows.appendChild(tr);
      }
    }
  }

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    try {
      await api('/zones/' + encodeURIComponent(state.currentZone) + '/records', {
        method: 'PUT',
        body: JSON.stringify({ name: fd.get('name') || '@', type: fd.get('type'), value: fd.get('value'), ttl: Number(fd.get('ttl')) })
      });
      form.reset(); toast('saved'); await loadRows();
    } catch (e) { toast(e.message, true); }
  });

  await loadRows();
}

// ---- api keys ----------------------------------------------------------
async function renderKeys(view) {
  view.appendChild(tpl('tpl-keys'));
  const rows = view.querySelector('[data-role=rows]');
  const form = view.querySelector('#create-key');
  const newKey = view.querySelector('[data-role=new-key]');
  const load = async () => {
    rows.innerHTML = '';
    const ks = await api('/keys');
    for (const k of ks) {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td>${escapeHtml(k.name)}</td><td>${k.token_prefix}…</td><td>${fmtTs(k.created_at)}</td><td>${fmtTs(k.last_used_at)}</td><td class="actions"></td>`;
      const del = document.createElement('button'); del.textContent = 'revoke'; del.className = 'danger';
      del.addEventListener('click', async () => {
        if (!confirm(`Revoke ${k.name}?`)) return;
        try { await api('/keys/' + k.id, { method: 'DELETE' }); toast('revoked'); await load(); } catch (e) { toast(e.message, true); }
      });
      tr.querySelector('.actions').appendChild(del);
      rows.appendChild(tr);
    }
  };
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    try {
      const k = await api('/keys', { method: 'POST', body: JSON.stringify({ name: fd.get('name') }) });
      newKey.hidden = false; newKey.textContent = `Your new key (shown once): ${k.token}`;
      form.reset(); await load();
    } catch (e) { toast(e.message, true); }
  });
  await load();
}

// ---- users -------------------------------------------------------------
async function renderUsers(view) {
  view.appendChild(tpl('tpl-users'));
  const rows = view.querySelector('[data-role=rows]');
  const form = view.querySelector('#create-user');
  const load = async () => {
    rows.innerHTML = '';
    try {
      const us = await api('/users');
      for (const u of us) {
        const tr = document.createElement('tr');
        tr.innerHTML = `<td>${escapeHtml(u.email)}</td><td>${u.role}</td><td>${fmtTs(u.created_at)}</td><td>${fmtTs(u.last_login_at)}</td><td class="actions"></td>`;
        const del = document.createElement('button'); del.textContent = 'delete'; del.className = 'danger';
        del.addEventListener('click', async () => {
          if (!confirm(`Delete ${u.email}?`)) return;
          try { await api('/users/' + u.id, { method: 'DELETE' }); toast('deleted'); await load(); } catch (e) { toast(e.message, true); }
        });
        tr.querySelector('.actions').appendChild(del);
        rows.appendChild(tr);
      }
    } catch (e) {
      if (e.status === 403) rows.innerHTML = '<tr><td colspan=5>admin only</td></tr>';
      else toast(e.message, true);
    }
  };
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    try {
      await api('/users', { method: 'POST', body: JSON.stringify({ email: fd.get('email'), password: fd.get('password'), role: fd.get('role') }) });
      form.reset(); toast('user created'); await load();
    } catch (e) { toast(e.message, true); }
  });
  await load();
}

// ---- audit -------------------------------------------------------------
async function renderAudit(view) {
  view.appendChild(tpl('tpl-audit'));
  const rows = view.querySelector('[data-role=rows]');
  const es = await api('/audit?limit=200');
  for (const e of es) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${fmtTs(e.ts)}</td><td>${e.actor_user || e.actor_ip || ''}</td><td>${e.action}</td><td>${escapeHtml(e.target_id || '')}</td><td>${e.outcome}</td>`;
    rows.appendChild(tr);
  }
}

// ---- health ------------------------------------------------------------
async function renderHealth(view) {
  view.appendChild(tpl('tpl-health'));
  const pre = view.querySelector('[data-role=report]');
  const r = await fetch('/readyz').then((r) => r.json()).catch(() => ({}));
  pre.textContent = JSON.stringify(r, null, 2);
}

// ---- helpers -----------------------------------------------------------
function fmtTs(s) { if (!s) return ''; try { return new Date(s).toLocaleString(); } catch { return s; } }
function escapeHtml(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }

// ---- boot --------------------------------------------------------------
loadAuth();
if (state.token) {
  // Verify token still works.
  api('/auth/me').then(renderShell).catch(() => { clearAuth(); renderLogin(); });
} else {
  renderLogin();
}
