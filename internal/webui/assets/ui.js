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
    const params = new URLSearchParams(location.search);
    let path='/ui/fragments/'+selected();
    if(selected()==='knowledge') {if(params.has('page_id'))path='/ui/fragments/page?page_id='+encodeURIComponent(params.get('page_id'));else if(params.has('parent_id'))path+='?parent_id='+encodeURIComponent(params.get('parent_id'));}
    htmx.ajax('GET', path, {target: screen, swap: 'innerHTML'}).catch(() => {});
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
    const snippetButton=event.target.closest('[data-toggle-snippet]');
    if(snippetButton) {const expanded=snippetButton.closest('.evidence-card').classList.toggle('snippet-expanded');snippetButton.setAttribute('aria-expanded',String(expanded));snippetButton.textContent=expanded?'Collapse snippet':'Show full snippet';return;}
    const toggleSources = event.target.closest('[data-toggle-sources]');
    if(toggleSources) {const grid=screen.querySelector('.knowledge-grid');if(grid){if(matchMedia('(max-width:639px)').matches) {if(grid.classList.toggle('mobile-sources-open'))grid.querySelector('.source-panel')?.scrollIntoView({block:'start'});}else grid.classList.toggle('sources-hidden');}return;}
    if(event.target.closest('[data-toggle-catalog]')) {screen.querySelector('.knowledge-grid')?.classList.toggle('catalog-open');return;}
    if(event.target.closest('[data-toggle-json]')) {const json=document.getElementById('response-json');if(json) json.hidden=!json.hidden;return;}
    if(event.target.closest('[data-export-json]')) {const data=document.getElementById('response-json-data');if(data){const url=URL.createObjectURL(new Blob([data.textContent],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='knowl-search.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}return;}
    const page = event.target.closest('a[href]');
    if(page && event.button===0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {const u=new URL(page.href,location.href);if(u.origin===location.origin && u.pathname==='/ui/knowledge' && (u.searchParams.has('page_id') || u.searchParams.has('parent_id'))) {event.preventDefault();history.pushState(null,'',u.pathname+u.search);navigate();return;}}
    const link = event.target.closest('[data-screen]'); if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault(); history.pushState(null, '', '/ui/' + link.dataset.screen); navigate();
  });
  document.addEventListener('htmx:configRequest', event => {
    if (!token || !protectedURL(event.detail.path) || event.detail.verb !== 'get') {event.preventDefault(); return;}
    event.detail.headers.Authorization = 'Bearer ' + token;
  });
  document.addEventListener('htmx:beforeRequest', event => {
    const url = protectedURL(event.detail.requestConfig.path);
    if (!token || !url) {event.preventDefault(); return;}
    // Catalog, All pages, and continuation controls replace the same screen.
    // Advance before aborting so canceled requests cannot display an error.
    if (event.detail.target === screen && ['/ui/fragments/knowledge', '/ui/fragments/page'].includes(url.pathname)) abortRequests();
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
    if(token && pending.get(xhr)===generation && xhr.getResponseHeader('X-Knowl-Error')==='snapshot_changed') {
      const url=protectedURL(event.detail.requestConfig.path);if(url && url.searchParams.has('cursor')) {url.searchParams.delete('cursor');htmx.ajax('GET',url.pathname+url.search,{target:screen,swap:'innerHTML'}).catch(()=>{});}
    }
    pending.delete(xhr);
  });
  document.addEventListener('htmx:afterSettle', () => {
    for(const snippet of screen.querySelectorAll('.evidence-snippet')) {const toggle=snippet.nextElementSibling;if(toggle?.matches('[data-toggle-snippet]')) toggle.hidden=snippet.scrollHeight<=snippet.clientHeight;}
  });
  addEventListener('popstate', navigate);
  addEventListener('pagehide', disconnect);
  addEventListener('pageshow', event => {if(event.persisted) disconnect();});
  navigate();
})();
