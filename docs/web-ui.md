# Read your wiki in a browser

Knowl's optional web UI lets you read published knowledge, inspect its saved
sources, search for evidence, and follow processing. It shares the Go service's
HTTP listener and persistent workspace. Four server-rendered screens use
bundled, pinned local assets; a normal Go build needs no Node runtime or
frontend tooling.

## Connect

Web access is disabled by default. Merge this overlay into an existing valid
Knowl configuration:

```yaml
knowl:
  web:
    enabled: true
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
```

Set the named environment variable to your operator secret and restart
`knowl start`. Enabled web access requires a nonempty token. With the default
listener, open `http://127.0.0.1:8080/ui/`, enter the token, and select
**Connect**. The token stays in memory for the current document; reload or
**Disconnect** clears it. Navigation within the connected UI keeps the session.
The shell and assets are public, while workspace reads require authentication.

Use HTTPS through a trusted proxy for access beyond loopback. The token also
permits ingestion through the existing agent API, even though the UI only reads.
Keep local secret overrides out of Git. See
[configuration and security](operations.md#optional-web-ui-and-operator-reads)
and [container setup](sidecar.md#optional-browser-access).

## Knowledge

Knowledge opens the canonical wiki root and renders its actual index body.
Follow catalog and page links, use the breadcrumb to return through verified
catalogs, or choose **All pages**. Back and Forward preserve navigation within
the connected document. Direct links and reload use the root and any verified
current catalog; deeper ancestry is not inferred from file paths.

**Details** starts closed and contains page identity, digest/version, and
available type, description, tags, status, trust and stale metadata.
**Page sources** starts closed at every screen width; open it explicitly to
inspect saved revisions supporting a leaf page. A leaf with no references says
so. Catalog indexes have no source panel. On a small screen, **Browse knowledge**
opens the catalog panel.

Select **Read saved source** to read immutable accepted text with its digest
and media type. This uses Knowl's saved revision and does not fetch upstream.
An unavailable revision is reported explicitly. **Open original** appears only
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
snapshot. The search is evidence retrieval, not a chat or generated answer.

## Operations

Read stored processing history, filter by status or source ID, and select an
operation. Details show available execution/retry facts, retrieval and selected
context reports, a plan digest/count summary, and correction facts. Selected
context describes what the maintainer read; it is not a changed-page list or
changeset preview. Plan corrections are separate from execution retries. Context
reports distinguish candidates excluded by the request byte budget from entries
omitted by reporting bounds. Missing older reports are unavailable rather than
zero.

A selected queued/running operation refreshes while the tab is visible. Polling
pauses in a hidden tab, backs off on transient failures, and stops on terminal
status, navigation, or disconnect. **Refresh** reloads the list. This screen
does not start work or retry failed operations.

## Sources

Select a configured source to inspect synchronization facts, accepted documents,
and maintenance status. Document rows distinguish the upstream head, accepted
revision, and processing revision. **View operation** opens a stored associated
operation when available.

Successful synchronization accepts data and reserves work; wiki processing may
still be queued or failed. A later failed sync preserves the last successful
sync and accepted revisions. Only a saved reconciled tombstone confirms
deletion; omission from a bounded page does not. Use **Next** controls for more
rows. General lists start at 50 rows, Operations at 10; continuation is bounded
and may need a refresh when the workspace changes or the service restarts.

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
3. In **Knowledge**, open **All pages** and choose a generated authentication
   page. Open **Page sources**, then **Read saved source**. The text is the
   accepted authentication document; it remains immutable if the source changes.
4. In **Search**, submit `session revocation JWT` across the wiki. Inspect the
   evidence and **View JSON**. With embeddings disabled, this query is searched
   locally. Follow an evidence title to read its current page.
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

After documentation changes are reviewed, regenerate the project's wiki through
its existing source configuration and maintainer workflow, from the repository
root:

```bash
task wiki:generate
```

This task resolves the pinned ACP runner, runs `knowl run --source knowl-docs`,
and validates the workspace. It requires the configured maintainer's
prerequisites and authentication. Inspect the generated pages and accepted raw
provenance separately before committing. `task wiki:validate` validates the
workspace without regeneration. Preserve accepted raw history when revising or
rolling back wiki output; operators own its Git publication.
