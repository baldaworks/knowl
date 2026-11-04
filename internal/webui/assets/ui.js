/* Trusted UI controller. Credentials and response generations never leave this closure. */
(() => {
  'use strict';
  let token = '', generation = 0, connectionState = 'disconnected', focusScreen = false;
  const authMode = document.body.dataset.authMode;
  const canRequest = () => connectionState === 'connecting' || connectionState === 'connected';
  let sourcesOpener = null, sourcesScroll = 0, rawOpener = null;
  let operationOpener = null, operationScroll = 0;
  let searchSnapshot = null, searchVisitSerial = 0, searchRendered = false;
  const searchSnapshotLimit = 256 * 1024;
  let treePending = 0, treeVisitSerial = 0, renderedTreeEntry = 0;
  const treeSnapshots = new Map(), treeNodeLimit = 1000, treeSnapshotLimit = 3;
  let detailRead = null;
  const pending = new Map();
  const wikiReads = new Map();
  let wikiBranchSerial = 0;
  let wikiAncestors = [], wikiReveal = false;
  let contextOpener = null, contextRestoreFocus = true;
  let poll = null, issuingPoll = false;
  const screen = document.getElementById('screen');
  const names = {knowledge: 'Knowledge', search: 'Search', operations: 'Operations', sources: 'Sources', statistics: 'Statistics'};
  const protectedPaths = new Set(['knowledge', 'page', 'wiki-directory', 'wiki-pages', 'source-revision', 'search', 'operations', 'operation', 'sources', 'source', 'statistics'].map(p => '/ui/fragments/' + p));
  function protectedURL(value) {
    try { const u = new URL(value, location.href); return u.origin === location.origin && !u.username && !u.password && protectedPaths.has(u.pathname) ? u : null; } catch { return null; }
  }
  function selected() { return Object.hasOwn(names, location.pathname.split('/').pop()) ? location.pathname.split('/').pop() : 'knowledge'; }
  function stopPolling() { if (!poll) return; clearTimeout(poll.timer); const xhr=poll.xhr; poll=null; if(xhr) {pending.delete(xhr);xhr.abort();} }
  function abortRequests() { stopPolling(); generation++; detailRead=null; for (const xhr of pending.keys()) xhr.abort(); pending.clear(); for(const read of wikiReads.values())finishWikiRead(read,true); }
  function message(text) { const div = document.createElement('div'); div.className = 'empty-state'; div.textContent = text; screen.replaceChildren(div); }
  const sidebar = document.getElementById('workspace-navigation');
  const navToggle = document.getElementById('navigation-toggle');
  const wikiNavigation = document.getElementById('wiki-navigation');
  const wikiTree = document.getElementById('wiki-tree');
  const wikiStatus = document.getElementById('wiki-tree-status');
  const contextCompact = matchMedia('(max-width:1199.98px)');
  const mobile = matchMedia('(max-width:991.98px)');
  const compact = matchMedia('(max-width:639px)');
  const pushMenu = adminlte.PushMenu.getOrCreateInstance(sidebar);
  const backgrounds = [...document.querySelectorAll('.app-header,.app-main')];
  // Browsers can drop focus to BODY before a breakpoint's change event fires.
  let responsiveFocus = null;
  document.addEventListener('focusin',event=>{
    responsiveFocus=event.target instanceof HTMLElement && (event.target.matches('#navigation-close') || screen.querySelector('#page-context')?.contains(event.target))?event.target:null;
  });
  function restoreResponsiveFocus() {
    if(responsiveFocus?.isConnected && !isVisible(responsiveFocus)) {
      const destination=responsiveFocus.id==='navigation-close'?navToggle:screen.querySelector('#context-toggle') || screen;
      if(isVisible(destination))destination.focus({preventScroll:true});
    }
  }
  function syncNavigation() {
    const connected = connectionState === 'connected';
    const open = connected && !pushMenu.isCollapsed();
    const modal = open && mobile.matches;
    const wasModal = sidebar.getAttribute('aria-modal') === 'true';
    backgrounds.forEach(element => {element.inert = modal;});
    if (!open && sidebar.contains(document.activeElement)) (connected ? navToggle : authMode === 'token' ? document.getElementById('operator-token') : document.querySelector('#connect-form button')).focus({preventScroll:true});
    sidebar.hidden = !connected; sidebar.inert = !open;
    sidebar.setAttribute('aria-hidden',String(!open));
    navToggle.hidden = !connected; navToggle.setAttribute('aria-expanded',String(open));
    if (modal) {sidebar.setAttribute('role','dialog');sidebar.setAttribute('aria-modal','true');if(!wasModal)(sidebar.querySelector('[aria-current="page"]') || document.getElementById('navigation-close')).focus();}
    else {sidebar.removeAttribute('role');sidebar.removeAttribute('aria-modal');}
    restoreResponsiveFocus();
  }
  sidebar.addEventListener('open.lte.push-menu',event=>{if(connectionState!=='connected')event.preventDefault();else closePageContext(false);});
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
    document.getElementById('disconnect').hidden=authMode!=='token' || (!connected && !connecting);
    document.getElementById('disconnect').textContent=connecting?'Cancel':'Disconnect';
    document.getElementById('connection-status').textContent=connected?'Connected':connecting?'Connecting…':'Disconnected';
    screen.hidden=!connected;
    const note=document.getElementById('connection-feedback');note.textContent=feedback;note.hidden=!feedback;
    const input=document.getElementById('operator-token');if(invalid)input.setAttribute('aria-invalid','true');else input.removeAttribute('aria-invalid');
    if(!connected)pushMenu.collapse();
    syncNavigation();
  }
  function disconnect(feedback = '', invalid = false, focus = true) {
    token = ''; abortRequests(); document.getElementById('operator-token').value = '';
    disposePageContext();sourcesOpener=null;rawOpener=null;operationOpener=null;searchSnapshot=null;searchRendered=false;treeSnapshots.clear();renderedTreeEntry=0;wikiTree.replaceChildren();wikiBranchSerial=0;wikiAncestors=[];wikiReveal=false;wikiStatus.hidden=true;wikiNavigation.hidden=true;screen.replaceChildren();
    setConnectionState('disconnected',feedback,invalid);
    if(focus)(authMode==='token'?document.getElementById('operator-token'):document.querySelector('#connect-form button')).focus({preventScroll:true});
  }
  function localMessage(target,text) {const note=document.createElement('div');note.className='empty-state';note.textContent=text;target.replaceChildren(note);}
  function addRawClose(target) {
    if(target.querySelector('[data-close-raw]'))return;
    const button=document.createElement('button');button.type='button';button.className='btn btn-sm btn-outline-secondary';button.dataset.closeRaw='';button.textContent='Close saved source';
    target.prepend(button);
  }
  function revealDetail(target) {
    if(!target?.isConnected || !target.getClientRects().length)return;
    const start=target.querySelector('.raw-heading strong,h2,.empty-state') || target;
    const range=document.createRange();range.selectNodeContents(start);
    let rect=target.matches('button,a[href]')?target.getBoundingClientRect():range.getBoundingClientRect();
    const errorMessage=target.querySelector('[data-error-code] p');
    if(errorMessage){range.selectNodeContents(errorMessage);const messageRect=range.getBoundingClientRect();rect={top:Math.min(rect.top,messageRect.top),bottom:Math.max(rect.bottom,messageRect.bottom)};}
    const firstDocument=target.id==='source-detail'?target.querySelector('.source-documents tbody .table-name'):null;
    if(firstDocument){const documentRect=firstDocument.getBoundingClientRect();rect={top:Math.min(rect.top,documentRect.top),bottom:Math.max(rect.bottom,documentRect.bottom)};}
    const headerBottom=Math.max(0,document.querySelector('.app-header').getBoundingClientRect().bottom);
    if(rect.top<headerBottom || rect.bottom>innerHeight) {
      target.scrollIntoView({block:'start',behavior:'instant'});
      const top=target.getBoundingClientRect().top;
      if(top<headerBottom)scrollBy({top:top-headerBottom-8,behavior:'instant'});
    }
    target.focus({preventScroll:true});
  }
  function requestFailed(target) {
    if(connectionState==='connecting' || authMode==='proxy')disconnect(authMode==='token'?'Connection failed. Check the server and token, then Connect to retry.':authMode==='proxy'?'Your access session may have expired. Reconnect through the configured identity provider.':'Connection failed. Retry the public knowledge view.');
    else {target.removeAttribute('aria-busy');localMessage(target,'Request failed. Select again or refresh this view to retry.');if(target.id==='raw-source')addRawClose(target);}
  }
  function load() {
    if (!canRequest()) return;
    const params = new URLSearchParams(location.search);
    let path='/ui/fragments/'+selected();
    if(selected()==='knowledge') {
      const query=new URLSearchParams();
      for(const key of ['page_id','parent_id','trail','view','limit','cursor'])if(params.has(key))query.set(key,params.get(key));
      if(params.has('page_id'))path='/ui/fragments/page';
      if(query.size)path+='?'+query;
    }
    const request=htmx.ajax('GET', path, {target: screen, swap: 'innerHTML'});
    const current=generation;
    request.then(()=>{
      if(canRequest() && generation===current && selected()==='operations' && params.has('operation_id')) htmx.ajax('GET','/ui/fragments/operation?operation_id='+encodeURIComponent(params.get('operation_id')),{target:'#operation-detail',swap:'innerHTML'}).catch(()=>{});
    }).catch(() => {});
  }
  function rememberSearch() {
    const result=screen.querySelector('#search-results');
    if(!searchRendered || !result?.querySelector('#response-json-data'))return;
    const markup=screen.innerHTML;
    const query=screen.querySelector('#query').value;
    if(markup.length+query.length>searchSnapshotLimit || new TextEncoder().encode(markup).length+new TextEncoder().encode(query).length>searchSnapshotLimit) {searchSnapshot=null;return;}
    const entry=++searchVisitSerial;
    history.replaceState({searchEntry:entry},'',location.href);
    searchSnapshot={entry,view:screen.cloneNode(true),query};
  }
  function navigate(event) {
    disposePageContext();
    if(screen.querySelector('.wiki-tree') && renderedTreeEntry) {
      const snapshot=document.createElement('div');
      snapshot.replaceChildren(...screen.childNodes);
      treeSnapshots.delete(renderedTreeEntry);
      treeSnapshots.set(renderedTreeEntry,snapshot);
      if(treeSnapshots.size>treeSnapshotLimit)treeSnapshots.delete(treeSnapshots.keys().next().value);
    }
    renderedTreeEntry=0;
    abortRequests(); sourcesOpener=null;rawOpener=null;operationOpener=null;focusScreen=true; const key = selected(); document.body.dataset.uiScreen=key;
    if(key!=='knowledge')wikiReveal=false;
    document.title = names[key] + ' · Knowl'; screen.setAttribute('aria-label', names[key]);
    wikiNavigation.hidden=connectionState!=='connected' || key!=='knowledge';
    document.getElementById('header-view').textContent = names[key];
    for (const link of document.querySelectorAll('[data-screen]')) {link.classList.toggle('active', link.dataset.screen === key); if(link.dataset.screen === key) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');}
    const params=new URLSearchParams(location.search);
    if(event?.type==='popstate' && canRequest() && key==='search' && searchSnapshot && Number.isSafeInteger(history.state?.searchEntry) && history.state.searchEntry===searchSnapshot.entry) {
      screen.replaceChildren(...searchSnapshot.view.cloneNode(true).childNodes);
      screen.querySelector('#query').value=searchSnapshot.query;
      htmx.process(screen);
      screen.focus({preventScroll:true});focusScreen=false;searchRendered=true;return;
    }
    if(key==='search') {searchSnapshot=null;searchRendered=false;}
    const treeEntry=history.state?.treeEntry;
    const snapshot=Number.isSafeInteger(treeEntry)?treeSnapshots.get(treeEntry):null;
    if(event?.type==='popstate' && canRequest() && key==='knowledge' && params.get('view')==='all' && !params.has('page_id') && snapshot?.hasChildNodes()) {
      treeSnapshots.delete(treeEntry);screen.replaceChildren(...snapshot.childNodes);adoptKnowledge();screen.focus({preventScroll:true});focusScreen=false;return;
    }
    message(canRequest() ? 'Loading view…' : authMode==='token' ? 'Connect to read knowledge.' : 'Reconnect to read knowledge.'); load();
  }
  function navigationState(url) {
    if(url.pathname!=='/ui/knowledge')return null;
    if(url.searchParams.get('view')==='all' && !url.searchParams.has('page_id'))return {treeEntry:++treeVisitSerial};
    return null;
  }
  function adoptKnowledge() {
    const nav=screen.querySelector('nav[aria-label="Breadcrumb"]');
    if(!nav)return;
    const state={};
    if(screen.querySelector('.wiki-tree')) {
      const prior=history.state?.treeEntry;
      renderedTreeEntry=Number.isSafeInteger(prior)&&prior>0?prior:++treeVisitSerial;
      treeVisitSerial=Math.max(treeVisitSerial,renderedTreeEntry);
      state.treeEntry=renderedTreeEntry;
    } else renderedTreeEntry=0;
    history.replaceState(state,'',location.href);
    adoptWikiNavigation();
  }
  function closePageContext(restoreFocus=true) {
    const panel=screen.querySelector('#page-context');
    const instance=panel && bootstrap.Offcanvas.getInstance(panel);
    contextRestoreFocus=restoreFocus;
    if(instance) {
      // Finish the native transition before removing its element. This lets
      // Bootstrap restore its own scroll lock and focus trap synchronously.
      instance.hide();
      panel.dispatchEvent(new Event('transitionend'));
      for(const backdrop of screen.querySelectorAll('.offcanvas-backdrop'))backdrop.dispatchEvent(new Event('transitionend'));
      instance.dispose();
    }
    if(!restoreFocus)contextOpener=null;
  }
  function disposePageContext() {
    closePageContext(false);
    for(const element of screen.querySelectorAll('#page-context .tab-pane,#page-context [data-bs-toggle="tab"]'))element.dispatchEvent(new Event('transitionend'));
    for(const tab of screen.querySelectorAll('#page-context [data-bs-toggle="tab"]'))bootstrap.Tab.getInstance(tab)?.dispose();
  }
  document.addEventListener('show.bs.offcanvas',event=>{
    if(event.target.id!=='page-context')return;
    contextOpener=event.relatedTarget || screen.querySelector('#context-toggle');contextRestoreFocus=true;
    if(mobile.matches)pushMenu.collapse();
  });
  document.addEventListener('shown.bs.offcanvas',event=>{
    if(event.target.id==='page-context')screen.querySelector('#context-toggle')?.setAttribute('aria-expanded','true');
  });
  document.addEventListener('hidden.bs.offcanvas',event=>{
    if(event.target.id!=='page-context')return;
    screen.querySelector('#context-toggle')?.setAttribute('aria-expanded','false');
    if(contextRestoreFocus && contextOpener?.isConnected && isVisible(contextOpener))contextOpener.focus({preventScroll:true});
    contextOpener=null;
  });
  contextCompact.addEventListener('change',()=>{
    const panel=screen.querySelector('#page-context');
    const focused=panel?.contains(document.activeElement)?document.activeElement:responsiveFocus;
    const inside=panel?.contains(focused);
    closePageContext(false);
    if(inside) {
      const destination=isVisible(focused)?focused:screen.querySelector('#context-toggle');
      (isVisible(destination)?destination:screen).focus({preventScroll:true});
    }
  });
  function updateWikiSelection() {
    const id=screen.querySelector('#article')?.dataset.pageId;
    for(const link of wikiTree.querySelectorAll('[data-wiki-page]')) {
      if(link.dataset.wikiPage!=='index') {
        const ancestors=[];
        let parent=link.closest('[data-page-node]')?.parentElement.closest('[data-page-node]');
        while(parent && ancestors.length<16) {ancestors.unshift(parent.dataset.pageNode);parent=parent.parentElement.closest('[data-page-node]');}
        ancestors.unshift('index');
        const url=new URL(link.href,location.href);
        if(ancestors.length<=16) {url.searchParams.set('parent_id',ancestors.at(-1));url.searchParams.set('trail',JSON.stringify(ancestors));}
        else {url.searchParams.delete('parent_id');url.searchParams.delete('trail');}
        link.href=url.pathname+url.search;
      }
      link.classList.toggle('active',link.dataset.wikiPage===id);
      if(link.dataset.wikiPage===id)link.setAttribute('aria-current','page');
      else link.removeAttribute('aria-current');
    }
  }
  function setWikiExpanded(caret,open) {
    const branch=caret?.parentElement.querySelector(':scope > [data-page-branch]');
    if(!branch)return;
    if(!branch.id)branch.id='wiki-branch-'+(++wikiBranchSerial);
    caret.setAttribute('aria-controls',branch.id);caret.setAttribute('aria-expanded',String(open));
    caret.parentElement.classList.toggle('menu-open',open);
    caret.setAttribute('aria-label',(open?'Collapse ':'Expand ')+(caret.nextElementSibling?.textContent || 'page'));
    branch.hidden=!open;
  }
  function adoptWikiNavigation() {
    wikiNavigation.hidden=connectionState!=='connected' || selected()!=='knowledge';
    if(wikiNavigation.hidden)return;
    wikiAncestors=[...screen.querySelectorAll('nav[aria-label="Breadcrumb"] a[href]')].map(link=>new URL(link.href,location.href).searchParams.get('page_id')).filter(id=>id && id!=='index').slice(0,16);
    wikiReveal=true;
    if(!wikiTree.hasChildNodes()) {
      const home=document.createElement('a');home.href='/ui/knowledge';home.dataset.wikiPage='index';home.className='nav-link';home.textContent='Home';
      const homeItem=document.createElement('li');homeItem.className='nav-item';homeItem.append(home);
      const homeList=document.createElement('ul');homeList.className='nav sidebar-menu flex-column';homeList.append(homeItem);
      const branch=document.createElement('div');branch.dataset.pageBranch='';branch.dataset.pageDirectory='';
      wikiTree.append(homeList,branch);
      readWikiBranch(branch,'/ui/fragments/wiki-pages?directory=',false);
    }
    updateWikiSelection();
    revealWikiAncestors();
  }
  function revealWikiAncestors() {
    if(!wikiReveal || !wikiTree.querySelector('[data-wiki-pages]'))return;
    for(const id of wikiAncestors) {
      const link=[...wikiTree.querySelectorAll('[data-wiki-page]')].find(node=>node.dataset.wikiPage===id);
      const caret=link?.previousElementSibling,branch=link?.nextElementSibling;
      // Do not scan additional sibling windows to discover a missing parent.
      if(!caret?.matches('[data-page-toggle]') || !branch) {wikiReveal=false;return;}
      if(branch.dataset.loaded==='true') {setWikiExpanded(caret,true);continue;}
      if(branch.getAttribute('aria-busy')==='true')return;
      if(branch.querySelector('[data-page-read-state]')) {wikiReveal=false;return;}
      setWikiExpanded(caret,true);readWikiBranch(branch,caret.dataset.pageUrl,false,caret);return;
    }
    wikiReveal=false;
  }
  function wikiCapacityAvailable() {
    if(wikiTree.querySelectorAll('[data-page-node]').length+screen.querySelectorAll('[data-wiki-path]').length+(wikiReads.size+treePending+1)*100+1<=treeNodeLimit) {wikiStatus.hidden=true;return true;}
    wikiStatus.hidden=false;wikiStatus.textContent='Wiki tree limit reached. Reconnect to start again.';
    return false;
  }
  function readWikiBranch(branch,value,append,button=null,preserveClosed=false) {
    const url=protectedURL(value);
    if(!branch?.isConnected || !wikiTree.contains(branch) || !url || url.pathname!=='/ui/fragments/wiki-pages' || !canRequest() || branch.getAttribute('aria-busy')==='true' || !wikiCapacityAvailable())return;
    if(!append)branch.hidden=preserveClosed;
    branch.querySelector(':scope > [data-page-read-state]')?.remove();
    const note=document.createElement('div');note.dataset.pageReadState='';note.className='small p-1';note.setAttribute('role','status');note.textContent='Loading pages…';
    const stage=document.createElement('div');stage.dataset.wikiStage='';stage.hidden=true;
    branch.append(note,stage);branch.setAttribute('aria-busy','true');
    if(button)button.disabled=true;
    const read={branch,url,append,button,note,stage,generation,ended:false};wikiReads.set(stage,read);
    // A queued HTMX request's promise may resolve before it starts. Commit
    // only from the trusted live stage's afterSwap event below.
    htmx.ajax('GET',url.pathname+url.search,{source:branch,target:stage,swap:'innerHTML'}).catch(()=>finishWikiRead(read,false));
  }
  function finishWikiRead(read,aborted) {
    if(read.ended)return;
    read.ended=true;wikiReads.delete(read.stage);
    const {branch,stage,button,note}=read;
    branch.removeAttribute('aria-busy');if(button?.isConnected)button.disabled=false;
    if(!branch.isConnected || !wikiTree.contains(branch)) {stage.remove();return;}
    const live=!aborted && canRequest() && generation===read.generation;
    const list=live?stage.querySelector(':scope > [data-wiki-pages]'):null;
    const addedCarets=list?[...list.querySelectorAll('[data-page-toggle]')]:[];
    const error=stage.querySelector('[data-error-code]');
    if(list && list.querySelectorAll('[data-page-node]').length<=100) {
      if(read.append) {
        const prior=branch.querySelector(':scope > [data-wiki-pages]');
        const continuation=button?.closest('[data-page-continuation]');
        if(!prior || !continuation || continuation.parentElement!==prior) {stage.remove();note.remove();return;}
        const first=list.firstElementChild;continuation.remove();prior.append(...list.childNodes);
        const focus=first?.querySelector('a,button');if(isVisible(focus))focus.focus({preventScroll:true});
      } else branch.replaceChildren(list);
      branch.dataset.loaded='true';note.remove();stage.remove();
      const caret=branch.parentElement.querySelector(':scope > [data-page-toggle]');
      if(caret)setWikiExpanded(caret,!branch.hidden);
      for(const child of addedCarets)setWikiExpanded(child,false);
      updateWikiSelection();revealWikiAncestors();return;
    }
    stage.remove();
    if(live && read.append && error?.dataset.errorCode==='snapshot_changed' && read.url.searchParams.has('cursor')) {
      const fresh=new URL(read.url),closed=branch.hidden;fresh.searchParams.delete('cursor');branch.replaceChildren();delete branch.dataset.loaded;
      readWikiBranch(branch,fresh.href,false,null,closed);return;
    }
    if(!canRequest()) {note.remove();return;}
    note.replaceChildren();
    if(error) {note.dataset.errorCode=error.dataset.errorCode;note.append(document.createTextNode(error.textContent+' '));}
    else note.append(document.createTextNode(aborted?'Page loading stopped. ':'Pages unavailable. '));
    const retry=document.createElement('button');retry.type='button';retry.className='btn btn-outline-light btn-sm';retry.dataset.pageRetry='';retry.dataset.pageUrl=read.url.pathname+read.url.search;retry.dataset.pageAppend=String(read.append);retry.textContent='Retry';note.append(retry);
  }
  document.getElementById('connect-form').addEventListener('submit', event => {
    event.preventDefault();
    if(authMode==='proxy') {location.assign(new URL(location.pathname+location.search,location.origin).href);return;}
    const value = document.getElementById('operator-token').value.trim(); disconnect('',false,false); if (authMode==='token' && !value) return;
    token = value; setConnectionState('connecting'); navigate();
  });
  document.getElementById('disconnect').addEventListener('click', () => disconnect());
  function treeCapacityAvailable() {
    const tree=screen.querySelector('.wiki-tree');
    if(!tree)return false;
    if(tree.querySelectorAll('[data-wiki-path]').length+wikiTree.querySelectorAll('[data-page-node]').length+(treePending+wikiReads.size+1)*100+1<=treeNodeLimit)return true;
    const note=tree.querySelector('.wiki-tree-limit');
    if(note){note.hidden=false;note.textContent='Tree limit reached. Open a page or refresh All pages to start again.';}
    return false;
  }
  function readTree(button,target,swap,onSuccess) {
    const url=protectedURL(button.dataset.directoryUrl);
    if(!url || url.pathname!=='/ui/fragments/wiki-directory' || !treeCapacityAvailable())return;
    const requestGeneration=generation;
    treePending++;button.disabled=true;
    htmx.ajax('GET',url.pathname+url.search,{target,swap}).then(()=>{
      if(canRequest() && generation===requestGeneration && !target.querySelector('[data-error-code]'))onSuccess();
    }).catch(()=>{}).finally(()=>{
      treePending--;
      if(target.isConnected && target.classList.contains('wiki-tree-branch') && target.hasChildNodes()) {target.hidden=false;button.setAttribute('aria-expanded','true');}
      if(button.isConnected)button.disabled=false;
    });
  }
  document.addEventListener('click', event => {
    const pageToggle=event.target.closest('[data-page-toggle]');
    if(pageToggle && wikiTree.contains(pageToggle)) {
      wikiReveal=false;
      const branch=pageToggle.parentElement.querySelector(':scope > [data-page-branch]');
      if(!branch)return;
      if(branch.dataset.loaded==='true')setWikiExpanded(pageToggle,branch.hidden);
      else {setWikiExpanded(pageToggle,true);readWikiBranch(branch,pageToggle.dataset.pageUrl,false,pageToggle);}
      return;
    }
    const pageMore=event.target.closest('[data-page-more]'),pageRetry=event.target.closest('[data-page-retry]');
    if((pageMore || pageRetry) && wikiTree.contains(pageMore || pageRetry)) {
      const control=pageMore || pageRetry,branch=control.closest('[data-page-branch]');
      const append=!!pageMore || pageRetry?.dataset.pageAppend==='true';
      const continuation=append?branch?.querySelector(':scope > [data-wiki-pages] > [data-page-continuation] > [data-page-more]'):null;
      readWikiBranch(branch,control.dataset.pageUrl,append,continuation || control);return;
    }
    const headingLink=event.target.closest('#outline a[href^="#"],.document a[href^="#"]');
    if(headingLink && event.button===0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {
      let id;try {id=decodeURIComponent(new URL(headingLink.href,location.href).hash.slice(1));}catch{return;}
      const heading=document.getElementById(id);
      if(heading && screen.querySelector('.document')?.contains(heading)) {
        event.preventDefault();closePageContext(false);heading.tabIndex=-1;heading.focus({preventScroll:true});heading.scrollIntoView({block:'start',behavior:'instant'});
        const top=heading.getBoundingClientRect().top,header=document.querySelector('.app-header').getBoundingClientRect().bottom;
        if(top<header)scrollBy({top:top-header-8,behavior:'instant'});
        history.replaceState(history.state,'',location.pathname+location.search+'#'+encodeURIComponent(id));return;
      }
    }
    const folder=event.target.closest('[data-tree-expand]');
    if(folder){
      const branch=folder.nextElementSibling;
      if(!branch)return;
      if(branch.dataset.loaded==='true') {branch.hidden=!branch.hidden;folder.setAttribute('aria-expanded',String(!branch.hidden));}
      else readTree(folder,branch,'innerHTML',()=>{branch.dataset.loaded='true';branch.hidden=false;folder.setAttribute('aria-expanded','true');});
      return;
    }
    const more=event.target.closest('[data-tree-more]');
    if(more){
      const item=more.closest('li'),list=item?.parentElement,previous=item?.previousElementSibling;
      readTree(more,item,'outerHTML',()=>{
        if(!list?.isConnected)return;
        const next=previous?.nextElementSibling || list.firstElementChild;
        if(!next?.hasAttribute('data-wiki-path'))return;
        const focus=next.querySelector('a,button') || next;
        if(focus===next)next.tabIndex=-1;
        focus.focus();
      });
      return;
    }
    if(event.target.closest('[data-close-raw]')) {
      abortRequests();const raw=screen.querySelector('#raw-source');raw?.replaceChildren();raw?.removeAttribute('aria-busy');
      const opener=rawOpener?.isConnected?rawOpener:screen.querySelector('[data-toggle-sources]') || screen;
      rawOpener=null;revealDetail(opener);return;
    }
    if(event.target.closest('[data-return-operation]')) {
      const opener=operationOpener?.isConnected && screen.contains(operationOpener)?operationOpener:null;
      if(opener){scrollTo({top:operationScroll,behavior:'instant'});opener.focus({preventScroll:true});}
      else {const heading=screen.querySelector('[data-operation-list-heading]');heading?.scrollIntoView({block:'start',behavior:'instant'});heading?.focus({preventScroll:true});}
      return;
    }
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
        grid.classList.toggle('sources-hidden',!open);
        grid.classList.toggle('mobile-sources-open',open);
        if(open && compact.matches){sourcesOpener=toggleSources;sourcesScroll=scrollY;grid.querySelector('.source-panel').scrollIntoView({block:'start',behavior:'instant'});grid.querySelector('[data-close-sources]').focus({preventScroll:true});}
        if(!open && (closeSources || grid.querySelector('.source-panel').contains(document.activeElement))){const opener=sourcesOpener || screen.querySelector('[data-toggle-sources]');if(compact.matches)scrollTo({top:sourcesScroll,behavior:'instant'});sourcesOpener=null;revealDetail(opener);}
        syncDisclosures();
      }return;
    }
    const jsonToggle=event.target.closest('[data-toggle-json]');
    if(jsonToggle) {const json=document.getElementById('response-json');if(json){json.hidden=!json.hidden;jsonToggle.setAttribute('aria-expanded',String(!json.hidden));}return;}
    if(event.target.closest('[data-export-json]')) {const data=document.getElementById('response-json-data');if(data){const url=URL.createObjectURL(new Blob([data.textContent],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='knowl-search.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}return;}
    const page = event.target.closest('a[href]');
    if(page && event.button===0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && !page.target && !page.hasAttribute('download')) {const u=new URL(page.href,location.href);if(u.origin===location.origin && u.pathname==='/ui/knowledge') {event.preventDefault();if(connectionState!=='connected')return;if(selected()==='search')rememberSearch();if(mobile.matches)pushMenu.collapse();history.pushState(navigationState(u),'',u.pathname+u.search);navigate();return;}}
    const link = event.target.closest('[data-screen]'); if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault(); if(connectionState!=='connected')return;if(mobile.matches)pushMenu.collapse();history.pushState(null, '', '/ui/' + link.dataset.screen); navigate();
  });
  document.addEventListener('htmx:configRequest', event => {
    if (!canRequest() || !protectedURL(event.detail.path) || event.detail.verb !== 'get') {event.preventDefault(); return;}
    if(authMode==='token')event.detail.headers.Authorization = 'Bearer ' + token;
    const path=protectedURL(event.detail.path).pathname;
  });
  document.addEventListener('htmx:beforeRequest', event => {
    const url = protectedURL(event.detail.requestConfig.path);
    if (!canRequest() || !url) {event.preventDefault(); return;}
    if(event.detail.target.hasAttribute('data-wiki-stage') && (!event.detail.target.isConnected || !wikiTree.contains(event.detail.target) || !wikiReads.has(event.detail.target))) {event.preventDefault();return;}
    if(url.pathname==='/ui/fragments/search' && event.detail.target.id==='search-results') {searchRendered=false;searchSnapshot=null;}
    // Whole-screen replacements cancel older reads, including catalog continuation.
    // Advance before aborting so canceled requests cannot display an error.
    if (event.detail.target === screen || event.detail.target.id==='raw-source' || url.pathname==='/ui/fragments/source') {
      if(event.detail.target===screen){operationOpener=null;disposePageContext();}
      abortRequests();
    }
    if(url.pathname==='/ui/fragments/operation') {
      if(issuingPoll && poll) poll.xhr=event.detail.xhr;
      else abortRequests();
    }
    pending.set(event.detail.xhr, generation);
    const target=event.detail.target;
    if(!issuingPoll && ['operation-detail','source-detail','raw-source'].includes(target.id)) {
      focusScreen=false;detailRead={xhr:event.detail.xhr,target,generation};
      if(target.id==='operation-detail') {
        operationOpener=event.detail.elt?.matches('.operation-select') && screen.contains(event.detail.elt)?event.detail.elt:null;
        if(operationOpener)operationScroll=scrollY;
      }
      if(target.id==='raw-source') {
        const record=event.detail.elt.closest('.source-record');
        if(record && screen.contains(record)){rawOpener=event.detail.elt;record.after(target);}
      }
      target.setAttribute('aria-busy','true');
      localMessage(target,{ 'operation-detail':'Loading operation…','source-detail':'Loading source…','raw-source':'Loading saved source…'}[target.id]);
      if(target.id==='raw-source')addRawClose(target);
      revealDetail(target);
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
    if (!canRequest() || pending.get(xhr) !== generation) {event.detail.shouldSwap = false; event.preventDefault(); return;}
    if(event.detail.target.hasAttribute('data-wiki-stage') && (!event.detail.target.isConnected || !wikiTree.contains(event.detail.target) || !wikiReads.has(event.detail.target))) {event.detail.shouldSwap=false;event.preventDefault();return;}
    const requestedURL=protectedURL(event.detail.requestConfig.path),responseURL=protectedURL(xhr.responseURL);
    const redirected=requestedURL&&responseURL&&(requestedURL.pathname!==responseURL.pathname||requestedURL.search!==responseURL.search);
    if (!requestedURL || !responseURL || (authMode==='proxy' && (xhr.getResponseHeader('Location')!==null || redirected))) {if(authMode==='proxy')disconnect('Your access session expired. Reconnect through the configured identity provider.');event.detail.shouldSwap = false; event.preventDefault(); return;}
    if (xhr.status === 401) {disconnect(authMode==='token'?'Token was rejected or the session expired. Paste the exact token value without extra quotes, backticks, or asterisks from copied formatting, then Connect again.':'Your access session expired. Reconnect through the configured identity provider.',authMode==='token'); event.detail.shouldSwap = false; event.preventDefault(); return;}
    if(xhr.getResponseHeader('X-Knowl-Fragment')!=='1'){event.detail.shouldSwap=false;event.preventDefault();requestFailed(event.detail.target);return;}
    if(poll?.xhr===xhr && (xhr.status===0 || xhr.status>=500)) {event.detail.shouldSwap=false;event.preventDefault();return;}
    const error = xhr.getResponseHeader('X-Knowl-Error');
    const trustedErrors={invalid_request:400,limit_invalid:400,cursor_invalid:400,catalog_not_found:404,directory_not_found:404,page_not_found:404,source_revision_not_found:404,operation_not_found:404,source_not_found:404,snapshot_changed:409,read_limit_exceeded:413,unsupported_format:415,retrieval_failed:503,capability_unavailable:503,not_ready:503,workspace_unavailable:503};
    const trusted=trustedErrors[error]===xhr.status;
    if(connectionState==='connecting' && (xhr.status===200 || trusted)) {setConnectionState('connected');if(!mobile.matches)pushMenu.expand();}
    if (trusted) {event.detail.shouldSwap = true; event.detail.isError = false;}
    else if (xhr.status >= 400) {
      event.detail.shouldSwap = false; event.preventDefault(); requestFailed(event.detail.target);
    }
  });
  document.addEventListener('htmx:afterRequest', event => {
    const xhr = event.detail.xhr;
    const current=canRequest() && pending.get(xhr)===generation;
    const wikiRead=wikiReads.get(event.detail.target);
    if(current && wikiRead)finishWikiRead(wikiRead,false);
    const wasPoll=poll?.xhr===xhr;
    if(current && wasPoll) {
      poll.xhr=null;
      if(xhr.status===0 || xhr.status>=500) {poll.delay=Math.min(poll.delay*2,30000);const note=screen.querySelector('[data-poll-message]');if(note)note.textContent='Refresh unavailable. Retrying shortly.';schedulePoll();}
      else if(xhr.status===200) adoptOperation();
      else stopPolling();
    } else if(current && xhr.status===200 && protectedURL(event.detail.requestConfig.path)?.pathname==='/ui/fragments/operation') adoptOperation();
    if (current && !wasPoll) {event.detail.target.removeAttribute('aria-busy');if(xhr.status===0)requestFailed(event.detail.target);}
    if(canRequest() && pending.get(xhr)===generation && xhr.getResponseHeader('X-Knowl-Error')==='snapshot_changed') {
      const url=protectedURL(event.detail.requestConfig.path);if(url && url.pathname!=='/ui/fragments/wiki-pages' && url.searchParams.has('cursor')) {
        url.searchParams.delete('cursor');
        if(url.pathname==='/ui/fragments/wiki-directory') {
          const directory=url.searchParams.get('directory')||'';
          const branch=[...screen.querySelectorAll('[data-wiki-directory]')].find(node=>node.dataset.wikiDirectory===directory);
          if(branch)htmx.ajax('GET',url.pathname+url.search,{target:branch,swap:'outerHTML'}).catch(()=>{});
        } else {
          if(selected()==='knowledge'){const shell=new URL(location.href);shell.searchParams.delete('cursor');history.replaceState(history.state,'',shell.pathname+shell.search);}
          htmx.ajax('GET',url.pathname+url.search,{target:screen,swap:'innerHTML'}).catch(()=>{});
        }
      }
    }
    if(current && !wasPoll && ['raw-source','source-detail','operation-detail'].includes(event.detail.target.id))revealDetail(event.detail.target);
    pending.delete(xhr);
  });
  function isVisible(element) {return !!element?.isConnected && !!element.getClientRects().length && getComputedStyle(element).display!=='none' && getComputedStyle(element).visibility!=='hidden';}
  function syncDisclosures() {
    for(const [button,panel] of [['[data-toggle-sources]','.source-panel']]) {
      const control=screen.querySelector(button),element=screen.querySelector(panel);
      control?.setAttribute('aria-expanded',String(isVisible(element)));
      if(element && !isVisible(element) && element.contains(document.activeElement)) (isVisible(control)?control:screen).focus({preventScroll:true});
    }
    restoreResponsiveFocus();
  }
  compact.addEventListener('change',syncDisclosures);
  document.addEventListener('htmx:afterSwap', event => {
    const wikiRead=wikiReads.get(event.detail.target);
    if(wikiRead && canRequest() && pending.get(event.detail.xhr)===generation)finishWikiRead(wikiRead,false);
    if(event.detail.target===screen && selected()==='knowledge')adoptKnowledge();
    if(canRequest() && pending.get(event.detail.xhr)===generation && poll?.xhr!==event.detail.xhr && ['raw-source','source-detail','operation-detail'].includes(event.detail.target.id)) {
      if(event.detail.target.id==='raw-source')addRawClose(event.detail.target);
      revealDetail(event.detail.target);
    }
  });
  document.addEventListener('htmx:afterSettle', event => {
    if(canRequest() && selected()==='search' && event.detail.target.id==='search-results' && screen.contains(event.detail.target) && event.detail.xhr.status===200 && event.detail.target.querySelector('#response-json-data'))searchRendered=true;
    syncDisclosures();
    if(canRequest() && detailRead?.xhr===event.detail.xhr && detailRead.generation===generation) {revealDetail(detailRead.target);detailRead=null;}
    if(focusScreen && connectionState==='connected'){(mobile.matches?screen.querySelector('#article-title') || screen:screen).focus({preventScroll:true});focusScreen=false;}
    for(const snippet of screen.querySelectorAll('.evidence-snippet')) {const toggle=snippet.nextElementSibling;if(toggle?.matches('[data-toggle-snippet]')) toggle.hidden=snippet.scrollHeight<=snippet.clientHeight;}
  });

  function schedulePoll(delay) {
    if(!poll || !canRequest() || document.hidden || poll.xhr) return;
    clearTimeout(poll.timer);
    poll.timer=setTimeout(()=>{
      if(!poll || !canRequest() || document.hidden || poll.xhr) return;
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
  addEventListener('pageshow', event => {if(event.persisted){if(authMode==='token')disconnect();else{setConnectionState('connecting');navigate();}}});
  if(authMode==='token')setConnectionState('disconnected');else setConnectionState('connecting');navigate();
})();
