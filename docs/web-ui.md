# Read your wiki in a browser

Knowl's optional web UI lets you read published knowledge, inspect its saved
sources, search for evidence, follow processing, and inspect current database
statistics. It shares the Go service's HTTP listener and persistent workspace.
Five server-rendered screens use
bundled, pinned local assets; a normal Go build needs no Node runtime or
frontend tooling.

## Connect

Web access is disabled by default. Merge this overlay into an existing valid
Knowl configuration:

```yaml
knowl:
  web:
    enabled: true
    auth_mode: token
    access_mode: read_only
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
```

Set the named environment variable to your operator secret and restart
`knowl start`. Token-mode web access requires a nonempty token. With the default
listener, open `http://127.0.0.1:8080/ui/`, enter the token, and select
**Connect**. The token stays in memory for the current document; reload or
**Disconnect** clears it. Navigation within the connected UI keeps the session.
The shell and assets are public, while workspace reads require authentication.

Use HTTPS through a trusted proxy for access beyond loopback. The token also
permits ingestion only on API/MCP facades explicitly configured read-write; the
UI itself only reads. Public and proxy-authenticated Web profiles are described
in the [access-control guide](access-control.md).
Keep local secret overrides out of Git. See
[configuration and security](operations.md#optional-web-ui-and-operator-reads)
and [container setup](service.md#optional-browser-access).

## Knowledge

Knowledge opens the published root page. **Home** in the sidebar returns to it.
The sidebar shows pages in their published path hierarchy: select a title to
open that page, or use its separate caret to expand or collapse children without
changing the article. A directory's `index.md` represents one parent page, not a
folder plus another index row. Technical directories without an index are
transparent: their pages appear beneath the nearest actual parent page.

The tree is independent of links in page content. An index can contain an
authored table of contents, additional prose, or no body at all and still have
children. Its complete permitted Markdown body renders once in the article;
only an exact leading title already shown in the article header is omitted.
The UI does not add another directory or member list to that body. Published
empty and prose indexes are readable under the existing structural safety
checks; strict OKF publication and maintenance validation remain unchanged.
A page readable in the browser can therefore still fail strict publish
validation.

Breadcrumbs link only to actual parent index pages verified along the selected
page's path. Missing technical-directory indexes are skipped. Direct URLs,
Search evidence, reload, and browser Back/Forward open the requested page; when
an ancestor cannot be verified within the read bounds, the breadcrumb remains
shorter. No whole-wiki parent search is needed.

Branches load lazily. **More** loads another bounded window, **Retry** repeats
the failed branch read, and a changed continuation restarts that branch without
replacing the article. Broad or deep branches can reach a read limit. Pending
windows reserve space within the 1,000-node browser tree limit; reconnect to
start a new tree if that limit is reached. Completed branches, expansion, and
tree scroll remain in the connected tab across page and screen navigation.
Reload, disconnect, or rejected authorization clears protected page/tree state
and cancels pending reads. Credentials, content, and tree state are not saved in
browser storage; HTMX retains its existing session URL path/query marker.

The page context has **Contents**, **Sources**, and **Details** tabs, with
Contents selected initially. Contents lists the displayed second- and third-level headings;
selecting a heading scrolls and focuses it without reloading the page. Details
shows identity, digest/version, and available type, description, tags, status,
trust and stale metadata. Sources lists the page's saved references, or states
that none are recorded, including for an index page.

At widths of at least 1200px, context stays beside the article in a sticky card.
Below 1200px, **About this page** opens the same context in a drawer. Below
992px, the main navigation and page tree use the navigation drawer. Close or
Escape returns focus to the opener; selecting a page on mobile closes navigation
and focuses its title. Wide tables and code scroll locally without widening the
page.

**All pages** remains an optional file inventory for browsing literal directory
paths. Its folders expand separately, **More files** continues a branch, and
`index.md` appears as an ordinary file. This compatibility view is separate from
the primary page tree. Browser Back can restore its bounded loaded view within
the same connected tab; reload or disconnect clears it.

Select **Read saved source** to read immutable accepted text with its digest
and media type. This uses Knowl's saved revision and does not fetch upstream.
The selected reader opens immediately after its source record and focuses its
loading, accepted text, or error. **Close saved source** clears the reader and
returns focus to the read button. An unavailable revision is reported explicitly.
**Open original** appears only
when provenance contains an allowed, credential-free HTTP/HTTPS URL. A local
filesystem source may have no original URL; its saved text remains available.
Normal browsing does not automatically load remote images or upstream content.

Source references support the page as a whole. They are not sentence-level
citations. Pages are current canonical content, while accepted sources preserve
immutable history; see [workspace semantics](workspace.md#reading-pages-and-accepted-sources-in-the-browser).

## Search

Enter a query in the blank labeled input and select **Search** to search the
whole configured wiki. Each submission
uses the existing retrieval path once. No search runs while typing or merely
opening the screen. The disclosure above the results tells you whether
embeddings are enabled: submitting an embedding-enabled query sends it to the
configured embedding provider. Browsing pages and saved sources does not.

Evidence stays in server order with its original snippets and source references.
The retrieval badge reports lexical, hybrid, degraded, or failed retrieval when
those diagnostics are available. A successful empty result differs from a
failed retrieval. Missing diagnostics are shown as unavailable.

**View JSON** and **Export JSON** expose the same safe response used for the
cards, without another retrieval or the operator token. Select an evidence title
to read its current page, which may differ from the earlier search
snapshot. After a successful search, open an evidence page in the same tab and
use browser Back to restore the last query and results without another
retrieval. Only one bounded results view is kept in memory; an oversized view
cannot be restored. Opening a fresh Search view, reloading, or disconnecting
starts with a blank query and results. The search is evidence retrieval, not a
chat or generated answer.

## Operations

Read stored processing history, filter by status or source ID, and select an
operation. Full operation IDs are visible in the list and available in the
detail's expandable identity. **Back to operations** returns you to the selected
row, or the list heading when there is no selected row. Details show available
execution/retry facts, retrieval and selected context reports, a plan digest/count
summary, and correction facts. Selected
context describes what the maintainer read; it is not a changed-page list or
changeset preview. Plan corrections are separate from execution retries. Context
reports distinguish candidates excluded by the request byte budget from entries
omitted by reporting bounds. Missing older reports are unavailable rather than
zero.

Selecting an operation reveals and focuses its details, including loading or
errors in stacked layouts. A selected queued/running operation refreshes while
the tab is visible. Background polling keeps reading position and focus. Polling
pauses in a hidden tab, backs off on transient failures, and stops on terminal
status, navigation, or disconnect. **Refresh** reloads the first list page while
keeping the selected status and source filters. This screen
does not start work or retry failed operations.

## Sources

Select a configured source to open its saved documents first. Document rows
distinguish the upstream head, accepted revision, and processing revision.
**View operation** opens a stored associated operation when available. **Sync
and processing** history follows below the document table. Selecting a source
or **Next documents** reveals and focuses the requested details, including
loading and failures, beneath tall lists.

Successful synchronization accepts data and reserves work; wiki processing may
still be queued or failed. A later failed sync preserves the last successful
sync and accepted revisions. Only a saved reconciled tombstone confirms
deletion; omission from a bounded page does not. Use **Next** controls for more
rows. General lists start at 50 rows, Operations at 10; continuation is bounded
and may need a refresh when the workspace changes or the service restarts.

## Statistics

Statistics reads the database when the page is opened and presents the current
state from one consistent, read-only database view. It does not save a copy,
record history, draw time-series charts, or poll in the background. Select
**Refresh snapshot**, reload, or open the page again to take another reading.

The knowledge section is limited to the configured scope. It reports pages,
links, configured sources, accepted source documents, operations grouped by
lifecycle status, projection cursors, and embedding coverage. Embedding
statistics validate the configured model identity and dimensions without
loading vector values.

The storage section is deployment-wide rather than scope-bound. It identifies
the backend, reports the database size when the backend exposes it, and gives
exact row counts and available size facts for a fixed allowlist of Knowl tables.
There is no table-name or scope selector. SQLite reports the main database
allocation; optional `dbstat` details are shown only when the SQLite build
provides them. PostgreSQL reports database and relation sizes when its catalog
permissions allow those reads. An unavailable optional metric is labeled with
its reason and does not prevent the remaining statistics from rendering.

The page uses the same public, token, or proxy authentication mode as the rest
of the enabled Web UI and is always read-only. Enabling it adds no API or MCP
endpoint and requires no database migration.

## Source-to-wiki walkthrough

This walkthrough uses one checked-in source document in a fresh temporary
workspace. It requires the Go version in `go.mod`, an OpenAI API key, and a
model available to that key. Maintenance invokes the real configured provider;
generated page names and wording can vary. Browse the resulting pages rather
than assuming a fixed page ID. Existing project configuration and knowledge
stay in their original directories.

From the repository root, build the binary and copy the source:

```bash
KNOWL_UI_DEMO=$(mktemp -d)
go build -o "$KNOWL_UI_DEMO/knowl" ./cmd/knowl
mkdir -p "$KNOWL_UI_DEMO/sources" "$KNOWL_UI_DEMO/.config/knowl"
cp examples/source-to-wiki/sources/authentication-service.md \
  "$KNOWL_UI_DEMO/sources/"
export OPENAI_API_KEY='your-api-key'
export OPENAI_MODEL='a-model-available-to-your-account'
export KNOWL_OPERATOR_TOKEN='choose-a-local-secret'
```

Save this as `$KNOWL_UI_DEMO/.config/knowl/config.yaml`:

```yaml
runtime:
  providers:
    openai:
      type: openai
      openai:
        api_key: ${OPENAI_API_KEY}
        model: ${OPENAI_MODEL}
knowl:
  provider: openai
  workspace:
    path: knowledge
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  scope: local
  server:
    listen_addr: 127.0.0.1:8086
  web:
    enabled: true
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
  embeddings:
    enabled: false
  sources:
    - id: engineering-docs
      type: filesystem
      filesystem:
        root: sources
        include: ["authentication-service.md"]
        flavor: markdown
```

Initialize, synchronize and drain maintenance, validate, then start the service:

```bash
cd "$KNOWL_UI_DEMO"
./knowl init
./knowl run --source engineering-docs
./knowl validate
./knowl source status engineering-docs
./knowl start
```

Keep the service running and open `http://127.0.0.1:8086/ui/`:

1. Connect using the operator token you chose above.
2. In **Sources**, select `engineering-docs`. Check the accepted revision and
   completed processing for `authentication-service.md`. If processing failed,
   inspect the associated operation before proceeding.
3. In **Knowledge**, select a generated authentication page in the sidebar tree,
   using the separate carets and **More** as needed. Select the **Sources** tab;
   on a narrow screen, first open **About this page**. Select **Read saved
   source**. The text is the accepted authentication document; it remains
   immutable if the source changes.
4. In **Search**, submit `session revocation JWT` across the wiki. Inspect the
   evidence and **View JSON**. With embeddings disabled, this query is searched
   locally. Follow an evidence title to read its current page, then use browser
   Back to restore the query and results without searching again.
5. In **Sources**, open the document's **View operation** link. Compare its saved
   processing facts with the accepted revision. Legacy absent reports remain
   unavailable.

Stop the server with Ctrl-C. Keep the temporary workspace to inspect `raw/` and
`wiki/`; it is not published automatically. Operators own Git review, commits,
and remote publication.

For reproducible source acceptance, maintenance, and retrieval verification
without a live model, run the existing deterministic fixture from the repository
root. It uses a test maintainer and a fresh workspace; it does not launch the
browser or mutate the checked-in showcase:

```bash
go test -count=1 ./examples/source-to-wiki \
  -run '^TestSourceToWikiShowcaseEndToEnd$'
```

## Maintaining this project's wiki

See [Knowl maintains its own documentation wiki](examples/own-docs-wiki.md) for
the actual source configuration, generation commands, prerequisites and review
of saved provenance.
