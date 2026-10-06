/* Trusted UI controller. Credentials and response generations never leave this closure. */
(() => {
  'use strict';
  let token = '', generation = 0;
  const pending = new Map();
  const screen = document.getElementById('screen');
  const names = {knowledge: 'Knowledge', search: 'Search', operations: 'Operations', sources: 'Sources'};
  const descriptions = {knowledge: 'Read your wiki and inspect the sources behind it.', search: 'Find evidence in your project knowledge.', operations: 'Follow processing and inspect the facts from each attempt.', sources: 'See what was synchronized and what has been processed.'};
  const protectedPaths = new Set(['knowledge', 'page', 'source-revision', 'search', 'operations', 'operation', 'sources', 'source'].map(p => '/ui/fragments/' + p));
  function protectedURL(value) {
    try { const u = new URL(value, location.href); return u.origin === location.origin && !u.username && !u.password && protectedPaths.has(u.pathname) ? u : null; } catch { return null; }
  }
  function selected() { return Object.hasOwn(names, location.pathname.split('/').pop()) ? location.pathname.split('/').pop() : 'knowledge'; }
  function abortRequests() { generation++; for (const xhr of pending.keys()) xhr.abort(); pending.clear(); }
  function message(text) { const div = document.createElement('div'); div.className = 'empty-state'; div.textContent = text; screen.replaceChildren(div); }
  function disconnect() {
    token = ''; abortRequests(); document.getElementById('operator-token').value = '';
    document.getElementById('connection-card').hidden = false; document.getElementById('disconnect').hidden = true;
    document.getElementById('connection-status').textContent = 'Disconnected'; message('Connect to load workspace data.');
  }
  function load() {
    if (!token) return;
    htmx.ajax('GET', '/ui/fragments/' + selected(), {target: screen, swap: 'innerHTML'}).catch(() => {});
  }
  function navigate() {
    abortRequests(); const key = selected();
    document.title = names[key] + ' · Knowl'; document.getElementById('screen-title').textContent = names[key];
    document.getElementById('header-view').textContent = names[key]; document.getElementById('screen-description').textContent = descriptions[key];
    for (const link of document.querySelectorAll('[data-screen]')) {link.classList.toggle('active', link.dataset.screen === key); if(link.dataset.screen === key) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');}
    message(token ? 'Loading workspace…' : 'Connect to load workspace data.'); load();
  }
  document.getElementById('connect-form').addEventListener('submit', event => {
    event.preventDefault(); const value = document.getElementById('operator-token').value.trim(); disconnect(); if (!value) return;
    token = value; document.getElementById('connection-card').hidden = true; document.getElementById('disconnect').hidden = false;
    document.getElementById('connection-status').textContent = 'Connected'; navigate();
  });
  document.getElementById('disconnect').addEventListener('click', disconnect);
  document.addEventListener('click', event => {
    const link = event.target.closest('[data-screen]'); if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault(); history.pushState(null, '', '/ui/' + link.dataset.screen); navigate();
  });
  document.addEventListener('htmx:configRequest', event => {
    if (!token || !protectedURL(event.detail.path) || event.detail.verb !== 'get') {event.preventDefault(); return;}
    event.detail.headers.Authorization = 'Bearer ' + token;
  });
  document.addEventListener('htmx:beforeRequest', event => {
    if (!token || !protectedURL(event.detail.requestConfig.path)) {event.preventDefault(); return;}
    pending.set(event.detail.xhr, generation);
  });
  document.addEventListener('htmx:beforeSwap', event => {
    const xhr = event.detail.xhr;
    if (!token || pending.get(xhr) !== generation || !protectedURL(xhr.responseURL)) {event.detail.shouldSwap = false; event.preventDefault(); return;}
    if (xhr.status === 401) {disconnect(); event.detail.shouldSwap = false; event.preventDefault(); return;}
    const error = xhr.getResponseHeader('X-Knowl-Error');
    if (error && [400,403,404,409,413,415,422,500,503].includes(xhr.status)) {event.detail.shouldSwap = true; event.detail.isError = false;}
    else if (xhr.status >= 400) {
      event.detail.shouldSwap = false; event.preventDefault(); message('Workspace request failed. Reconnect to retry.');
    }
  });
  document.addEventListener('htmx:afterRequest', event => {
    const xhr = event.detail.xhr;
    if (token && pending.get(xhr) === generation && xhr.status === 0) message('Workspace request failed. Reconnect to retry.');
    pending.delete(xhr);
  });
  addEventListener('popstate', navigate);
  addEventListener('pagehide', disconnect);
  addEventListener('pageshow', event => {if(event.persisted) disconnect();});
  navigate();
})();
