const referenceFixtures = {"results": "<div class=\"results-toolbar\"><div><strong>3 evidence items</strong><span class=\"text-secondary ms-2\">in server order</span></div><span class=\"status-badge status-primary\"><span class=\"status-dot\"></span>Hybrid retrieval</span><button class=\"btn btn-sm btn-light\" data-bs-toggle=\"collapse\" data-bs-target=\"#response-json\">View JSON</button></div><div class=\"collapse mb-3\" id=\"response-json\"><pre class=\"json-preview\"><code>{\n  \"example\": true,\n  \"evidence\": [\n    {\n      \"page_id\": \"concepts/source-synchronization\",\n      \"snippet\": \"Synchronization, source acceptance, and knowledge processing are separate stages.\"\n    },\n    {\n      \"page_id\": \"concepts/content-and-trust-boundaries\",\n      \"snippet\": \"Accepted raw revisions are immutable.\"\n    },\n    {\n      \"page_id\": \"concepts/service-operations\",\n      \"snippet\": \"Source status preserves the last attempt and last successful synchronization.\"\n    }\n  ],\n  \"retrieval\": {\n    \"mode\": \"hybrid\"\n  }\n}</code></pre></div><article class=\"card evidence-card\"><div class=\"card-body\"><div class=\"evidence-heading\"><span class=\"evidence-number\">01</span><a class=\"evidence-title\" href=\"index.html\" hx-get=\"index.html\" hx-select=\"#screen\" hx-target=\"#screen\" hx-swap=\"outerHTML\" hx-push-url=\"true\">Source synchronization</a><span class=\"page-type\">CONCEPT</span></div><p class=\"evidence-snippet\">Synchronization, source acceptance, and knowledge processing are separate stages. A successful sync can still have maintenance waiting in the queue.</p><div class=\"evidence-citations\"><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m10 13 4-4M8 15l-2 2a3 3 0 0 1-4-4l5-5a3 3 0 0 1 4 0m2 1 2-2a3 3 0 0 1 4 4l-5 5a3 3 0 0 1-4 0\"/></svg><span>Workspace guide</span><code>project-docs@3a68ce3</code></div><div class=\"evidence-bottom\"><span>concepts/source-synchronization</span><a class=\"\" href=\"index.html\" hx-get=\"index.html\" hx-select=\"#screen\" hx-target=\"#screen\" hx-swap=\"outerHTML\" hx-push-url=\"true\">Open current page <svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m9 5 7 7-7 7\"/></svg></a></div></div></article><article class=\"card evidence-card\"><div class=\"card-body\"><div class=\"evidence-heading\"><span class=\"evidence-number\">02</span><a class=\"evidence-title\" href=\"index.html\" hx-get=\"index.html\" hx-select=\"#screen\" hx-target=\"#screen\" hx-swap=\"outerHTML\" hx-push-url=\"true\">Content and trust boundaries</a><span class=\"page-type\">CONCEPT</span></div><p class=\"evidence-snippet\">Accepted raw revisions are immutable. Generated knowledge remains traceable to saved source revisions.</p><div class=\"evidence-citations\"><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m10 13 4-4M8 15l-2 2a3 3 0 0 1-4-4l5-5a3 3 0 0 1 4 0m2 1 2-2a3 3 0 0 1 4 4l-5 5a3 3 0 0 1-4 0\"/></svg><span>Workspace guide</span><code>project-docs@3a68ce3</code></div><div class=\"evidence-bottom\"><span>concepts/content-and-trust-boundaries</span><a class=\"\" href=\"index.html\" hx-get=\"index.html\" hx-select=\"#screen\" hx-target=\"#screen\" hx-swap=\"outerHTML\" hx-push-url=\"true\">Open current page <svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m9 5 7 7-7 7\"/></svg></a></div></div></article><article class=\"card evidence-card\"><div class=\"card-body\"><div class=\"evidence-heading\"><span class=\"evidence-number\">03</span><a class=\"evidence-title\" href=\"index.html\" hx-get=\"index.html\" hx-select=\"#screen\" hx-target=\"#screen\" hx-swap=\"outerHTML\" hx-push-url=\"true\">Service operations</a><span class=\"page-type\">CONCEPT</span></div><p class=\"evidence-snippet\">Source status preserves the last attempt and the last successful synchronization. Maintenance outcome is reported independently.</p><div class=\"evidence-citations\"><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m10 13 4-4M8 15l-2 2a3 3 0 0 1-4-4l5-5a3 3 0 0 1 4 0m2 1 2-2a3 3 0 0 1 4 4l-5 5a3 3 0 0 1-4 0\"/></svg><span>Operations guide</span><code>project-docs@3a68ce3</code></div><div class=\"evidence-bottom\"><span>concepts/service-operations</span><a class=\"\" href=\"index.html\" hx-get=\"index.html\" hx-select=\"#screen\" hx-target=\"#screen\" hx-swap=\"outerHTML\" hx-push-url=\"true\">Open current page <svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m9 5 7 7-7 7\"/></svg></a></div></div></article>", "raw": "<div class=\"raw-view\"><div class=\"raw-heading\"><strong>Saved source</strong><button class=\"btn btn-sm btn-icon\" data-action=\"close-raw\" aria-label=\"Close saved source\"><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m6 6 12 12M18 6 6 18\"/></svg></button></div><p class=\"source-meta\">Workspace guide \u00b7 revision <code>3a68ce3</code></p><pre><code># Workspace semantics\n\nAccepted raw sources are immutable.\nTheir identity includes the source,\ndocument, and accepted revision.\n\nSynchronization accepts new data.\nMaintenance processes that data\ninto canonical wiki pages.\n\nConfirmed deletion retains saved\nraw revisions and derived knowledge.</code></pre><span class=\"raw-caption\">Showing the saved text, not the upstream document.</span></div>", "operationDetail": "<div class=\"card operation-detail\"><div class=\"card-header\"><div><div class=\"eyebrow\">MAINTENANCE OPERATION</div><h2 class=\"detail-title\">Maintain Workspace guide</h2></div><span class=\"status-badge status-success\"><span class=\"status-dot\"></span>Completed</span></div><div class=\"card-body\"><div class=\"detail-meta\"><div><span>Source</span><strong>Project documentation</strong></div><div><span>Document</span><strong>Workspace guide</strong></div><div><span>Accepted revision</span><code>3a68ce3</code></div></div><div class=\"section-label\">EXECUTION</div><div class=\"execution-facts\"><div><strong>1</strong><span>Execution attempt</span></div><div><strong>0</strong><span>Retries</span></div><div><strong>1</strong><span>Plan correction</span></div></div><div class=\"section-label\">RETRIEVAL</div><div class=\"diagnostic-row\"><span class=\"status-badge status-primary\"><span class=\"status-dot\"></span>Hybrid</span><span>Saved retrieval report \u00b7 attempt 1</span></div><div class=\"section-label\">SELECTED CONTEXT</div><div class=\"context-list\"><span><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"M4 3h6a3 3 0 0 1 3 3v15a4 4 0 0 0-4-2H4V3Zm16 0h-4a3 3 0 0 0-3 3v15a4 4 0 0 1 4-2h3V3Z\"/></svg>Source synchronization <small>Direct match</small></span><span><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"M4 3h6a3 3 0 0 1 3 3v15a4 4 0 0 0-4-2H4V3Zm16 0h-4a3 3 0 0 0-3 3v15a4 4 0 0 1 4-2h3V3Z\"/></svg>Content and trust boundaries <small>Neighbor</small></span><span><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"M4 3h6a3 3 0 0 1 3 3v15a4 4 0 0 0-4-2H4V3Zm16 0h-4a3 3 0 0 0-3 3v15a4 4 0 0 1 4-2h3V3Z\"/></svg>Service operations <small>Direct match</small></span></div><div class=\"diagnostic-note\">2 pages were excluded by the input budget. 1 additional context entry is omitted from this diagnostic list.</div><div class=\"subtle-note\">Context shows what the maintainer read. It is not a list of changed pages.</div><div class=\"section-label\">PLAN SUMMARY</div><dl class=\"plan-facts\"><dt>Plan digest</dt><dd><code>f29c0ed43a68\u2026</code></dd><dt>Files in plan</dt><dd>2</dd></dl><details class=\"diagnostic-details\"><summary>Correction details <span>1</span></summary><p>The planner received one structured correction request before its plan was accepted. This is separate from execution retries.</p></details></div></div>", "rawOperations": "<div class=\"raw-view\"><div class=\"raw-heading\"><strong>Saved source</strong><button class=\"btn btn-sm btn-icon\" data-action=\"close-raw\" aria-label=\"Close saved source\"><svg class=\"icon \" viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"1.65\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m6 6 12 12M18 6 6 18\"/></svg></button></div><p class=\"source-meta\">Operations guide \u00b7 revision <code>d128750</code></p><pre><code># Operational status\n\nThe latest source attempt and the\nlast successful synchronization\nare reported independently.\n\nAccepting a revision does not mean\nits maintenance has completed.\n\nSaved diagnostics describe a\nspecific processing attempt.</code></pre><span class=\"raw-caption\">Showing the saved text, not the upstream document.</span></div>"};
(() => {
  const toast = (text) => {
    document.querySelector('#reference-toast .toast-body').textContent = text;
    bootstrap.Toast.getOrCreateInstance(document.getElementById('reference-toast'), {delay: 3500}).show();
  };
  const init = () => {
    const screen = document.getElementById('screen');
    const view = screen.dataset.view;
    const label = {index:'Knowledge', search:'Search', operations:'Operations', sources:'Sources'}[view];
    document.title = label + ' · Knowl';
    document.getElementById('header-view').textContent = label;
    document.querySelectorAll('.sidebar-menu .nav-link').forEach(a => {
      const active = a.getAttribute('href') === view + '.html';
      a.classList.toggle('active', active);
      if (active) a.setAttribute('aria-current','page'); else a.removeAttribute('aria-current');
    });
    document.getElementById('reference-state').value = 'default';
  };
  document.addEventListener('htmx:afterSwap', init);
  document.addEventListener('htmx:historyRestore', init);
  document.addEventListener('click', event => {
    const control = event.target.closest('[data-action]');
    if (!control) return;
    const action = control.dataset.action;
    if (action === 'toggle-sources') {
      const grid = document.querySelector('.knowledge-grid');
      if (innerWidth < 640) {
        grid?.classList.toggle('mobile-sources-open');
        if (grid?.classList.contains('mobile-sources-open')) grid.querySelector('.source-panel').scrollIntoView({behavior:'smooth',block:'start'});
      } else {
        grid?.classList.toggle('sources-hidden');
      }
    } else if (action === 'close-raw') {
      document.getElementById('raw-source').replaceChildren();
    } else if (action === 'raw-fallback' && location.protocol === 'file:') {
      event.preventDefault();
      document.getElementById('raw-source').innerHTML = control.dataset.refSource === 'operations' ? referenceFixtures.rawOperations : referenceFixtures.raw;
    } else if (action === 'related') {
      toast('Reference page: related-page navigation follows this layout.');
    } else if (action === 'all-pages') {
      const article = document.querySelector('.knowledge-article');
      article.innerHTML = '<div class="card-header"><h2 class="card-title">All pages</h2><span class="small text-secondary">5 pages</span></div><div class="list-group list-group-flush">' + ['Source synchronization','Content and trust boundaries','Public contract','Service operations','Releases'].map(name => '<a href="index.html" class="list-group-item list-group-item-action py-3">'+ name +'<span class="float-end text-secondary">Concept</span></a>').join('')+'</div>';
    } else if (action === 'refresh') {
      toast('Reference data refreshed.');
    } else if (action === 'retained') {
      toast('Confirmed deletion keeps accepted raw revisions and existing knowledge.');
    }
  });
  document.addEventListener('submit', event => {
    if (event.target.id === 'search-form' && location.protocol === 'file:') {
      event.preventDefault();
      document.getElementById('search-results').innerHTML = referenceFixtures.results;
    }
  });
  const filterOperations = () => {
    const status = document.getElementById('operation-status')?.value;
    const source = document.getElementById('operation-source')?.value;
    document.querySelectorAll('.operation-row').forEach(row => {
      row.hidden = Boolean((status && row.dataset.status !== status) || (source && row.dataset.source !== source));
    });
  };
  document.addEventListener('change', event => {
    if (['operation-status','operation-source'].includes(event.target.id)) filterOperations();
    if (event.target.id !== 'reference-state') return;
    const value = event.target.value;
    const view = document.getElementById('screen').dataset.view;
    if (value === 'default') { location.href = view + '.html'; return; }
    if (value === 'empty' && view === 'index') {
      document.querySelector('.knowledge-grid').innerHTML = '<div class="card w-100"><div class="empty-state"><h3>Your knowledge starts with a source.</h3><p>No published pages yet. Accepted documents appear here after processing.</p></div></div>';
    } else if (value === 'missing' && view === 'index') {
      document.querySelector('.source-panel .card-body').innerHTML = '<div class="source-record"><h3>Workspace guide</h3><span class="status-badge status-warning">Saved revision unavailable</span><p class="mt-3 text-secondary">This page references a saved revision that cannot be read. The current upstream document has not been substituted.</p></div>';
      document.querySelector('.knowledge-grid').classList.remove('sources-hidden');
      document.querySelector('.knowledge-grid').classList.add('mobile-sources-open');
    } else if (['degraded','unreported'].includes(value) && view === 'search') {
      const results = document.getElementById('search-results');
      results.innerHTML = referenceFixtures.results;
      const badge = results.querySelector('.status-badge');
      badge.className = 'status-badge status-warning';
      badge.textContent = value === 'degraded' ? 'Lexical fallback' : 'Diagnostics unavailable';
      const jsonView = results.querySelector('.json-preview code');
      const response = JSON.parse(jsonView.textContent);
      if (value === 'degraded') response.retrieval = {mode:'lexical',degraded:true,reason:'embedding_provider_unavailable'};
      else delete response.retrieval;
      jsonView.textContent = JSON.stringify(response,null,2);
      const note = document.createElement('div');
      note.className = 'alert alert-warning';
      note.textContent = value === 'degraded' ? 'Embedding provider unavailable. These results use lexical retrieval.' : 'This response does not include retrieval diagnostics. Its mode cannot be confirmed.';
      results.prepend(note);
    } else {
      toast('Open Knowledge for wiki/source states, or Search for retrieval states.');
    }
  });
  const selectOperation = row => {
    document.querySelectorAll('.operation-row').forEach(r => r.classList.toggle('selected-row', r === row));
    const panel = document.getElementById('operation-card');
    panel.innerHTML = referenceFixtures.operationDetail;
    panel.querySelector('.detail-title').textContent = row.querySelector('.table-name').textContent;
    panel.querySelector('.card-header > .status-badge').outerHTML = row.querySelector('.status-badge').outerHTML;
    const sourceValue = panel.querySelector('.detail-meta strong');
    sourceValue.textContent = row.dataset.source;
    if (row.dataset.status !== 'completed') {
      const body = panel.querySelector('.card-body');
      const section = document.createElement('div');
      section.className = 'alert alert-' + (row.dataset.status === 'failed' ? 'danger' : 'light');
      const notes = {running:'The maintainer is processing this revision. A completed plan is not available yet.', queued:'Revision accepted. Knowledge processing has not started yet.', failed:'Processing failed. The accepted source revision is retained.'};
      section.textContent = notes[row.dataset.status];
      body.replaceChildren(section);
      const note = document.createElement('p');
      note.className = 'subtle-note';
      note.textContent = 'Saved diagnostics are not available for this example attempt.';
      body.append(note);
    }
  };
  document.addEventListener('click', event => {
    const row = event.target.closest('.operation-row');
    if (row) selectOperation(row);
  });
  document.addEventListener('keydown', event => {
    const row = event.target.closest('.operation-row');
    if (row && ['Enter',' '].includes(event.key)) { event.preventDefault(); selectOperation(row); }
  });
  // A static reference fetches local fixtures only. It has no live API or provider.
  if (location.protocol === 'file:') {
    document.querySelectorAll('[hx-get]').forEach(node => {
      node.removeAttribute('hx-get');
    });
  }
  init();
})();
