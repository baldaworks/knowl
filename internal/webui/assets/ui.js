/* Trusted UI controller. Credentials and response generations never leave this closure. */
(() => {
  'use strict';
  let token = '', generation = 0, connectionState = 'disconnected', focusScreen = false;
  let sourcesOpener = null, sourcesScroll = 0;
  const pending = new Map();
  let poll = null, issuingPoll = false;
  const screen = document.getElementById('screen');
  const names = {knowledge: 'Knowledge', search: 'Search', operations: 'Operations', sources: 'Sources'};
  const descriptions = {knowledge: 'Read your wiki and inspect the sources behind it.', search: 'Find evidence in your project knowledge.', operations: 'Follow processing and inspect the facts from each attempt.', sources: 'See what was synchronized and what has been processed.'};
  const protectedPaths = new Set(['knowledge', 'page', 'source-revision', 'search', 'operations', 'operation', 'sources', 'source'].map(p => '/ui/fragments/' + p));
  function protectedURL(value) {
    try { const u = new URL(value, location.href); return u.origin === location.origin && !u.username && !u.password && protectedPaths.has(u.pathname) ? u : null; } catch { return null; }
  }
  function selected() { return Object.hasOwn(names, location.pathname.split('/').pop()) ? location.pathname.split('/').pop() : 'knowledge'; }
  function stopPolling() { if (!poll) return; clearTimeout(poll.timer); const xhr=poll.xhr; poll=null; if(xhr) {pending.delete(xhr);xhr.abort();} }
  function abortRequests() { stopPolling(); generation++; for (const xhr of pending.keys()) xhr.abort(); pending.clear(); }
  function message(text) { const div = document.createElement('div'); div.className = 'empty-state'; div.textContent = text; screen.replaceChildren(div); }
  const sidebar = document.getElementById('workspace-navigation');
  const navToggle = document.getElementById('navigation-toggle');
  const mobile = matchMedia('(max-width:991.98px)');
  const compact = matchMedia('(max-width:639px)');
  const pushMenu = adminlte.PushMenu.getOrCreateInstance(sidebar);
  const backgrounds = [...document.querySelectorAll('.app-header,.app-main,.app-footer')];
  // Browsers can drop focus to BODY before a breakpoint's change event fires.
  let responsiveFocus = null;
  document.addEventListener('focusin',event=>{
    responsiveFocus=event.target instanceof HTMLElement && event.target.matches('#navigation-close,[data-toggle-catalog]')?event.target:null;
  });
  function restoreResponsiveFocus() {
    if(responsiveFocus?.isConnected && !isVisible(responsiveFocus)) {
      const destination=responsiveFocus.id==='navigation-close'?navToggle:document.getElementById('screen-title');
      if(isVisible(destination))destination.focus({preventScroll:true});
    }
  }
  function syncNavigation() {
    const connected = connectionState === 'connected';
    const open = connected && !pushMenu.isCollapsed();
    const modal = open && mobile.matches;
    const wasModal = sidebar.getAttribute('aria-modal') === 'true';
    backgrounds.forEach(element => {element.inert = modal;});
    if (!open && sidebar.contains(document.activeElement)) (connected ? navToggle : document.getElementById('operator-token')).focus({preventScroll:true});
    sidebar.hidden = !connected; sidebar.inert = !open;
    sidebar.setAttribute('aria-hidden',String(!open));
    navToggle.hidden = !connected; navToggle.setAttribute('aria-expanded',String(open));
    if (modal) {sidebar.setAttribute('role','dialog');sidebar.setAttribute('aria-modal','true');if(!wasModal)(sidebar.querySelector('[aria-current="page"]') || document.getElementById('navigation-close')).focus();}
    else {sidebar.removeAttribute('role');sidebar.removeAttribute('aria-modal');}
    restoreResponsiveFocus();
  }
  sidebar.addEventListener('open.lte.push-menu',event=>{if(connectionState!=='connected')event.preventDefault();});
  sidebar.addEventListener('opened.lte.push-menu',syncNavigation);
  sidebar.addEventListener('collapsed.lte.push-menu',syncNavigation);
  document.getElementById('navigation-close').addEventListener('click',()=>pushMenu.collapse());
  sidebar.addEventListener('keydown',event=>{
    if(!mobile.matches || sidebar.inert)return;
    if(event.key==='Escape'){event.preventDefault();pushMenu.collapse();return;}
    if(event.key!=='Tab')return;
    const items=[...sidebar.querySelectorAll('a[href],button')].filter(e=>!e.hidden && e.getClientRects().length);
    const first=items[0],last=items.at(-1);
    if(event.shiftKey && document.activeElement===first){event.preventDefault();last.focus();}
    else if(!event.shiftKey && document.activeElement===last){event.preventDefault();first.focus();}
  });
  mobile.addEventListener('change',()=>{
    // The vendor retains sidebar-open on desktop; normalize via its shared API.
    if(mobile.matches)pushMenu.collapse();
    else if(pushMenu.isExplicitlyOpen()) {
      const focused=document.activeElement;
      pushMenu.collapse();
      if(connectionState==='connected')pushMenu.expand();
      if(sidebar.contains(focused) && isVisible(focused))focused.focus({preventScroll:true});
    }
    syncNavigation();
  });
  function setConnectionState(state, feedback = '', invalid = false) {
    connectionState=state;document.body.dataset.connectionState=state;
    const connected=state==='connected', connecting=state==='connecting';
    document.getElementById('connection-card').hidden=connected;
    document.getElementById('connection-card').setAttribute('aria-busy',String(connecting));
    document.querySelector('#connect-form button').disabled=connecting;
    document.getElementById('disconnect').hidden=!connected && !connecting;
    document.getElementById('disconnect').textContent=connecting?'Cancel':'Disconnect';
    document.getElementById('connection-status').textContent=connected?'Connected':connecting?'Connecting…':'Disconnected';
    document.getElementById('workspace-heading').hidden=!connected;screen.hidden=!connected;
    const note=document.getElementById('connection-feedback');note.textContent=feedback;note.hidden=!feedback;
    const input=document.getElementById('operator-token');if(invalid)input.setAttribute('aria-invalid','true');else input.removeAttribute('aria-invalid');
    if(!connected)pushMenu.collapse();
    syncNavigation();
  }
  function disconnect(feedback = '', invalid = false, focus = true) {
    token = ''; abortRequests(); document.getElementById('operator-token').value = '';
    sourcesOpener=null;screen.replaceChildren();
    setConnectionState('disconnected',feedback,invalid);
    if(focus)document.getElementById('operator-token').focus({preventScroll:true});
  }
  function localMessage(target,text) {const note=document.createElement('div');note.className='empty-state';note.textContent=text;target.replaceChildren(note);}
  function requestFailed(target) {
    if(connectionState==='connecting')disconnect('Connection failed. Check the server and token, then Connect to retry.');
    else {target.removeAttribute('aria-busy');localMessage(target,'Workspace request failed. Select again or refresh this view to retry.');}
  }
  function load() {
    if (!token) return;
    const params = new URLSearchParams(location.search);
    let path='/ui/fragments/'+selected();
    if(selected()==='knowledge') {if(params.has('page_id'))path='/ui/fragments/page?page_id='+encodeURIComponent(params.get('page_id'));else if(params.has('parent_id'))path+='?parent_id='+encodeURIComponent(params.get('parent_id'));}
    const request=htmx.ajax('GET', path, {target: screen, swap: 'innerHTML'});
    const current=generation;
    request.then(()=>{
      if(token && generation===current && selected()==='operations' && params.has('operation_id')) htmx.ajax('GET','/ui/fragments/operation?operation_id='+encodeURIComponent(params.get('operation_id')),{target:'#operation-detail',swap:'innerHTML'}).catch(()=>{});
    }).catch(() => {});
  }
  function navigate() {
    abortRequests(); sourcesOpener=null;focusScreen=true; const key = selected();
    document.title = names[key] + ' · Knowl'; document.getElementById('screen-title').textContent = names[key];
    document.getElementById('header-view').textContent = names[key]; document.getElementById('screen-description').textContent = descriptions[key];
    for (const link of document.querySelectorAll('[data-screen]')) {link.classList.toggle('active', link.dataset.screen === key); if(link.dataset.screen === key) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');}
    message(token ? 'Loading workspace…' : 'Connect to load workspace data.'); load();
  }
  document.getElementById('connect-form').addEventListener('submit', event => {
    event.preventDefault(); const value = document.getElementById('operator-token').value.trim(); disconnect('',false,false); if (!value) return;
    token = value; setConnectionState('connecting'); navigate();
  });
  document.getElementById('disconnect').addEventListener('click', () => disconnect());
  document.addEventListener('click', event => {
    const operationButton=event.target.closest('[data-open-operation]');
    if(operationButton){history.pushState(null,'','/ui/operations?operation_id='+encodeURIComponent(operationButton.dataset.openOperation));navigate();return;}
    const snippetButton=event.target.closest('[data-toggle-snippet]');
    if(snippetButton) {const expanded=snippetButton.closest('.evidence-card').classList.toggle('snippet-expanded');snippetButton.setAttribute('aria-expanded',String(expanded));snippetButton.textContent=expanded?'Collapse snippet':'Show full snippet';return;}
    const toggleSources = event.target.closest('[data-toggle-sources]');
    const closeSources = event.target.closest('[data-close-sources]');
    if(toggleSources || closeSources) {
      const grid=screen.querySelector('.knowledge-grid');
      if(grid){
        const open=closeSources?false:!isVisible(grid.querySelector('.source-panel'));
        if(compact.matches){grid.classList.toggle('mobile-sources-open',open);grid.classList.remove('sources-hidden');}
        else grid.classList.toggle('sources-hidden',!open);
        if(open && compact.matches){sourcesOpener=toggleSources;sourcesScroll=scrollY;grid.querySelector('.source-panel').scrollIntoView({block:'start',behavior:'instant'});grid.querySelector('[data-close-sources]').focus({preventScroll:true});}
        if(!open && (closeSources || grid.querySelector('.source-panel').contains(document.activeElement))){const opener=sourcesOpener || screen.querySelector('[data-toggle-sources]');opener?.focus({preventScroll:true});if(compact.matches)scrollTo({top:sourcesScroll,behavior:'instant'});sourcesOpener=null;}
        syncDisclosures();
      }return;
    }
    if(event.target.closest('[data-toggle-catalog]')) {screen.querySelector('.knowledge-grid')?.classList.toggle('catalog-open');syncDisclosures();return;}
    const jsonToggle=event.target.closest('[data-toggle-json]');
    if(jsonToggle) {const json=document.getElementById('response-json');if(json){json.hidden=!json.hidden;jsonToggle.setAttribute('aria-expanded',String(!json.hidden));}return;}
    if(event.target.closest('[data-export-json]')) {const data=document.getElementById('response-json-data');if(data){const url=URL.createObjectURL(new Blob([data.textContent],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='knowl-search.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}return;}
    const page = event.target.closest('a[href]');
    if(page && event.button===0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {const u=new URL(page.href,location.href);if(u.origin===location.origin && u.pathname==='/ui/knowledge' && (page.classList.contains('brand-link') || u.searchParams.has('page_id') || u.searchParams.has('parent_id'))) {event.preventDefault();if(connectionState!=='connected')return;if(mobile.matches)pushMenu.collapse();history.pushState(null,'',u.pathname+u.search);navigate();return;}}
    const link = event.target.closest('[data-screen]'); if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault(); if(connectionState!=='connected')return;if(mobile.matches)pushMenu.collapse();history.pushState(null, '', '/ui/' + link.dataset.screen); navigate();
  });
  document.addEventListener('htmx:configRequest', event => {
    if (!token || !protectedURL(event.detail.path) || event.detail.verb !== 'get') {event.preventDefault(); return;}
    event.detail.headers.Authorization = 'Bearer ' + token;
  });
  document.addEventListener('htmx:beforeRequest', event => {
    const url = protectedURL(event.detail.requestConfig.path);
    if (!token || !url) {event.preventDefault(); return;}
    // Whole-screen replacements cancel older reads, including catalog continuation.
    // Advance before aborting so canceled requests cannot display an error.
    if (event.detail.target === screen || event.detail.target.id==='raw-source' || url.pathname==='/ui/fragments/source') abortRequests();
    if(url.pathname==='/ui/fragments/operation') {
      if(issuingPoll && poll) poll.xhr=event.detail.xhr;
      else abortRequests();
    }
    pending.set(event.detail.xhr, generation);
    const target=event.detail.target;
    if(!issuingPoll && ['operation-detail','source-detail','raw-source'].includes(target.id)) {
      target.setAttribute('aria-busy','true');
      localMessage(target,{ 'operation-detail':'Loading operation…','source-detail':'Loading source…','raw-source':'Loading saved source…'}[target.id]);
      const selector='[hx-target="#'+target.id+'"]';
      const controls=[...screen.querySelectorAll(selector)].filter(control=>target.id==='raw-source' || control.matches('.operation-select,.source-select'));
      const selectedControl=controls.includes(event.detail.elt)?event.detail.elt:controls.find(control=>control.getAttribute('hx-get')===url.pathname+url.search);
      if(selectedControl) {
        for(const control of controls){control.classList.toggle('detail-selected',control===selectedControl);if(control===selectedControl)control.setAttribute('aria-current','true');else control.removeAttribute('aria-current');}
      }
    }
  });
  document.addEventListener('htmx:beforeSwap', event => {
    const xhr = event.detail.xhr;
    if (!token || pending.get(xhr) !== generation || !protectedURL(xhr.responseURL)) {event.detail.shouldSwap = false; event.preventDefault(); return;}
    if (xhr.status === 401) {disconnect('Token was rejected or the session expired. Paste the exact token value without extra quotes, backticks, or asterisks from copied formatting, then Connect again.',true); event.detail.shouldSwap = false; event.preventDefault(); return;}
    if(poll?.xhr===xhr && (xhr.status===0 || xhr.status>=500)) {event.detail.shouldSwap=false;event.preventDefault();return;}
    const error = xhr.getResponseHeader('X-Knowl-Error');
    const trustedErrors={invalid_request:400,limit_invalid:400,cursor_invalid:400,catalog_not_found:404,page_not_found:404,source_revision_not_found:404,operation_not_found:404,source_not_found:404,snapshot_changed:409,read_limit_exceeded:413,unsupported_format:415,retrieval_failed:503,capability_unavailable:503,not_ready:503,workspace_unavailable:503};
    const trusted=trustedErrors[error]===xhr.status;
    if(connectionState==='connecting' && (xhr.status===200 || trusted)) {setConnectionState('connected');if(!mobile.matches)pushMenu.expand();}
    if (trusted) {event.detail.shouldSwap = true; event.detail.isError = false;}
    else if (xhr.status >= 400) {
      event.detail.shouldSwap = false; event.preventDefault(); requestFailed(event.detail.target);
    }
  });
  document.addEventListener('htmx:afterRequest', event => {
    const xhr = event.detail.xhr;
    const current=token && pending.get(xhr)===generation;
    const wasPoll=poll?.xhr===xhr;
    if(current && wasPoll) {
      poll.xhr=null;
      if(xhr.status===0 || xhr.status>=500) {poll.delay=Math.min(poll.delay*2,30000);const note=screen.querySelector('[data-poll-message]');if(note)note.textContent='Refresh unavailable. Retrying shortly.';schedulePoll();}
      else if(xhr.status===200) adoptOperation();
      else stopPolling();
    } else if(current && xhr.status===200 && protectedURL(event.detail.requestConfig.path)?.pathname==='/ui/fragments/operation') adoptOperation();
    if (current && !wasPoll) {event.detail.target.removeAttribute('aria-busy');if(xhr.status===0)requestFailed(event.detail.target);}
    if(token && pending.get(xhr)===generation && xhr.getResponseHeader('X-Knowl-Error')==='snapshot_changed') {
      const url=protectedURL(event.detail.requestConfig.path);if(url && url.searchParams.has('cursor')) {url.searchParams.delete('cursor');htmx.ajax('GET',url.pathname+url.search,{target:screen,swap:'innerHTML'}).catch(()=>{});}
    }
    if(current && !wasPoll && xhr.status===200 && matchMedia('(max-width:639px)').matches && ['operation','source'].some(name=>protectedURL(event.detail.requestConfig.path)?.pathname==='/ui/fragments/'+name)) {event.detail.target.scrollIntoView({block:'start',behavior:'instant'});if(!wasPoll)event.detail.target.focus({preventScroll:true});}
    pending.delete(xhr);
  });
  function isVisible(element) {return !!element && getComputedStyle(element).display!=='none';}
  function syncDisclosures() {
    for(const [button,panel] of [['[data-toggle-catalog]','.catalog-panel'],['[data-toggle-sources]','.source-panel']]) {
      const control=screen.querySelector(button),element=screen.querySelector(panel);
      control?.setAttribute('aria-expanded',String(isVisible(element)));
      if(element && !isVisible(element) && element.contains(document.activeElement)) (isVisible(control)?control:document.getElementById('screen-title')).focus({preventScroll:true});
    }
    restoreResponsiveFocus();
  }
  compact.addEventListener('change',syncDisclosures);
  document.addEventListener('htmx:afterSettle', () => {
    syncDisclosures();
    if(focusScreen && connectionState==='connected'){document.getElementById('screen-title').focus({preventScroll:true});focusScreen=false;}
    for(const snippet of screen.querySelectorAll('.evidence-snippet')) {const toggle=snippet.nextElementSibling;if(toggle?.matches('[data-toggle-snippet]')) toggle.hidden=snippet.scrollHeight<=snippet.clientHeight;}
  });

  function schedulePoll(delay) {
    if(!poll || !token || document.hidden || poll.xhr) return;
    clearTimeout(poll.timer);
    poll.timer=setTimeout(()=>{
      if(!poll || !token || document.hidden || poll.xhr) return;
      issuingPoll=true;
      try {htmx.ajax('GET',poll.url,{target:'#operation-detail',swap:'innerHTML'}).catch(()=>{});} finally {issuingPoll=false;}
    },delay===undefined?poll.delay:delay);
  }
  function adoptOperation() {
    const card=screen.querySelector('[data-operation-id]');
    if(!card || !['queued','running'].includes(card.dataset.operationStatus)) {stopPolling();return;}
    const url=protectedURL(card.dataset.pollUrl);
    if(!url || url.pathname!=='/ui/fragments/operation') {stopPolling();return;}
    if(poll)clearTimeout(poll.timer);
    poll={url:url.pathname+url.search,delay:2000,timer:null,xhr:null};schedulePoll();
  }
  document.addEventListener('visibilitychange',()=>{
    if(!poll)return;
    if(document.hidden){clearTimeout(poll.timer);const xhr=poll.xhr;poll.xhr=null;if(xhr){pending.delete(xhr);xhr.abort();}}
    else schedulePoll(0);
  });
  addEventListener('popstate', navigate);
  addEventListener('pagehide', () => disconnect('',false,false));
  addEventListener('pageshow', event => {if(event.persisted) disconnect();});
  setConnectionState('disconnected'); navigate();
})();
