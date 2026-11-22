# Wiki UI reference

Isolated AdminLTE prototype for `knowl-5n2.1`, awaiting explicit visual approval
of its exact source version. Passing browser checks or screenshots do not approve
it. Start from the repository root:

```sh
python3 -m http.server 8093 --bind 127.0.0.1 --directory design
```

Open <http://127.0.0.1:8093/wiki-ui/>; Ctrl-C stops the server. No login, live
endpoint, build or dependency installation is needed. Choose another local port
and set `WIKI_REFERENCE_URL` for the runner if needed.

The existing `../web-ui/vendor/` supplies pinned AdminLTE 4.10.0 and Bootstrap
5.3.8. `../web-ui/assets/knowl-logo.png` supplies the existing owl with its restored
38px crop. Vendor styles supply theme and typography; local CSS only handles
layout, scroll, category controls and the established logo placement.

Included behavior:

- Persistent app menu and independently scrolling wiki category tree. Knowledge
  is demonstrated; the other app sections are disabled.
- Knowl Index links the four actual categories from `knowledge/wiki/index.md`.
  Their article titles and membership mirror the checked-in category indexes.
  Titles are already available from `CatalogChildren`; this is not a filename
  inventory. Returned children retain canonical-ID order.
- A category's title opens that category's own page. Its separate caret only
  expands or collapses its direct children. Subcategories work the same way;
  articles have ordinary title links. There is no separate `index.md` tree row.
- Category pages have short representative local introductions and simple
  **Подкатегории** / **Страницы** link lists, following MediaWiki's category
  behavior within the existing AdminLTE layout. These introductions are prototype
  examples. Production must render the complete authored index body using its
  existing page contract, alongside member navigation; it must not strip or
  replace authored content with a directory listing.
- Article bodies are frozen rendered snapshots of the eight checked-in concept
  pages, with local links bound to this finite fixture. Headings, lists, tables
  and code retain long-reading coverage. Wide tables use native Bootstrap
  responsive scroll containers, keeping the article within narrow viewports. Unavailable targets report a stable
  local status and retain the current article.
- The collapsed **Демонстрационный макет / Сценарии** control includes a clearly
  marked nested-category example: **Команда → Процессы → Рабочие договорённости**.
  The same article also appears directly under **Команда**. Selected occurrence
  and breadcrumbs follow the clicked category chain; file paths never invent
  ancestry. Expansion and tree scroll survive article changes.
- Other controlled examples show empty, loading, error and continuation in one
  **Учебная категория · пример**. Finish explicitly completes loading; Retry
  resolves the local failure; More pages adds the two remaining pages to that
  occurrence. Reset restores the real root. Reads, state and fixtures are bounded.
- Native context tabs provide Contents, Sources and Details. Saved source text,
  unavailable revision and zero-source examples are local demo provenance,
  supporting the whole page rather than individual statements. No upstream
  source is fetched. File identity remains available only in Details.
- Below 1200px the same context becomes a Bootstrap offcanvas; below 992px the
  native AdminLTE navigation drawer opens from the menu. Escape/Close restore
  the opener. Mobile selection closes navigation and focuses the article title;
  context heading links close their drawer and focus the heading.

State lasts until reload/reset. No API/model calls, storage, new runtime,
framework, backend/domain API, whole-wiki scan or arbitrary remote HTML is used.
The production catalog read capability already exists; Story 2 may use a thin
webui fragment over `CatalogChildren`. Production authentication, safe rendering,
cursor/snapshot/history validation and disconnect cleanup belong to Story 2.
Global finder, backlinks, graph controls and renderer enhancements are deferred.
Production and historical `design/web-ui/` assets remain outside this change.

Repeat the checks with the existing development-only Playwright/Chromium:

```sh
node --check design/wiki-ui/reference.js
node --check design/wiki-ui/review.cjs
node design/wiki-ui/review.cjs
# WIKI_REFERENCE_URL=http://127.0.0.1:8094/wiki-ui/ node design/wiki-ui/review.cjs
```

The runner asserts observable DOM and real actions, never raw source substrings:
category label versus caret, member links, nested title navigation, repeated-page
context, expansion/scroll, error/Retry/More, unavailable targets, source states,
details, heading anchors, mobile keyboard/focus, unique IDs, overflow and local
network boundaries. Viewports: 1440×1000, 1024×900, 390×844 and 320×844.

| Image in `review/` | Viewport | View |
| --- | --- | --- |
| `desktop-category-root.png` | 1440×1000 | Knowl Index and four real categories |
| `desktop-nested-category.png` | 1440×1000 | Expanded nested category and its own page |
| `desktop-article.png` | 1440×1000 | Long real article and preserved category expansion |
| `desktop-sources.png` | 1440×1000 | Saved and unavailable source revisions |
| `tablet-article.png` | 1024×900 | Article with persistent tree |
| `mobile-article.png` | 390×844 | Root reading layout |
| `mobile-tree.png` | 390×844 | Native category navigation drawer |
| `mobile-context.png` | 390×844 | Sources in the context drawer |
| `branch-empty.png` | 1440×1000 | Empty category example |
| `branch-loading.png` | 1440×1000 | Explicit loading/busy state |
| `branch-error.png` | 1440×1000 | Error and Retry |
| `branch-more.png` | 1440×1000 | Bounded continuation |
| `mobile-320.png` | 320×844 | Long article, bounded table scrolling |
| `logo-desktop.png` | 1440×1000 crop | Preserved owl and native branding |

The ordered source set is `README.md`, `index.html`, `reference.css`,
`reference.js`, `review.cjs`, relative to `design/wiki-ui/`. Hash each UTF-8
filename, a NUL, its raw bytes and a NUL in this order. Screenshots are separate
evidence, avoiding a digest cycle. Reproduce the SHA-256:

```sh
python3 - <<'PY'
from hashlib import sha256
from pathlib import Path
base = Path('design/wiki-ui')
digest = sha256()
for name in ['README.md', 'index.html', 'reference.css', 'reference.js', 'review.cjs']:
    digest.update(name.encode() + b'\0' + (base / name).read_bytes() + b'\0')
print(digest.hexdigest())
PY
```

Beads records the digest, version-bound checks, image paths and actual user
feedback/decision. Every source change requires renewed evidence and a new digest.
Integration waits for actual exact-version visual approval, separate Verify,
merged changes and required green GitHub checks. This handoff grants no approval
or commit/push/merge authority.
