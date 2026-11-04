Development-only browser checks for the embedded UI, run from the repository root
with the Go version declared in `go.mod`:

```sh
npm ci --prefix tools/webui-browser
npx --prefix tools/webui-browser playwright install chromium
go test -tags browser ./internal/webui \
  -run 'TestBrowserSecurity|TestBrowserRepairs|TestBrowserFacadeModes' -count=1
go test -tags browser ./internal/webui \
  -run 'TestBrowserKnowledgeSearch|TestBrowserOperationsSources' -count=1
go test -tags browser ./internal/webui \
  -run 'TestBrowserWikiSession|TestBrowserWikiLayout' -count=1
go test -tags browser ./cmd/knowl -run '^TestBrowserLiveSourceWiki$' -count=1
```

The tagged Go tests launch isolated HTTP fixtures using the production shell,
Markdown renderer, controller, and authentication/read boundary, then exercise
them in Chromium. Production Go builds and ordinary tests do not require Node.
The security test checks the persisted `pageshow` cleanup handler explicitly; it
does not assume Chromium will place a page into its back/forward cache.

`TestBrowserWikiSession` uses a deterministic typed reader to exercise page
selection, persisted branches and scroll, title/caret independence, More/Retry,
stale and delayed responses, concurrent window reservations, capacity, history,
responsive focus, and authorization cleanup. `TestBrowserWikiLayout` uses an
isolated copy of the checked-in wiki through the filesystem adapter, adding
physical AI pages and an empty parent index with a child. Its accepted-source
fixture keeps a v1 citation readable while a newer v2 also exists. It checks actual page
rows, authored content, context, headings, tables, and containment at
1440×1000, 1024×900, 390×844, and 320×844. The broader screen tests cover Search,
the optional All pages file inventory, saved revisions, and the other screens.
The CLI scenario exercises actual source acceptance, SQLite, publication,
authenticated page/source reading and Search without additional inference.

Capture the layout test's screenshots in a separate local directory:

```sh
KNOWL_BROWSER_ARTIFACT_DIR=/tmp/knowl-wiki-browser \
  go test -tags browser ./internal/webui -run TestBrowserWikiLayout -count=1 -v
```

The runner creates that directory and writes `wiki-integration-*.png` for its
root, expanded tree, article, context, and table scenarios. These are generated
test artifacts; the command does not update the approved design reference or
publish a site. `KNOWL_PLAYWRIGHT_MODULE` can select an existing Playwright module
when it is installed outside this development directory.
