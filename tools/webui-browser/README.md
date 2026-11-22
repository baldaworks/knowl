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
  -run 'TestBrowserWikiSession|TestBrowserWikiLayout|TestBrowserSharedTheme|TestBrowserNavigationContext' -count=1
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
physical AI pages, an empty parent index with a child, and arbitrary Unicode,
renamed, nested and empty groups with a custom frontmatter type. Its accepted-source
fixture keeps a v1 citation readable while a newer v2 also exists. It checks actual page
rows, authored content, context, headings, tables, and containment at
1440×1000, 1024×900, 390×844, and 320×844. The broader screen tests cover Search,
saved revisions and the other screens. `TestBrowserSharedTheme` compares all
five screens with the bundled vendor theme, including connected/disconnected
state. `TestBrowserNavigationContext` checks the supplied parent URL shape,
verified visited context, direct/reload/Back/Forward and invalid-context fallback.
The shared known-path discovery helper follows visible groups or actual index
pages without assuming starter names or flattening. The layout test also checks
that page and group labels keep a readable gap from their separate carets. Its
keyboard subtest holds the actual native inherited visibility transition on the
outline link, then resumes it to verify readiness, actual heading focus, hash,
drawer closure and immutable source reading at 1024/390/320.
An index leaf is checked through actual filesystem reads and rendered title
navigation without a disclosure caret.
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


## Real Balda acceptance

`balda.cjs` reads the actual public browser target through the existing
Playwright dependency. It does not mock APIs, rewrite responses, create wiki
content, deploy images or modify cluster resources. Run it only after reading
`../balda-assistant/AGENTS.md` and its Knowl runbook. It uses explicit kubeconfig
arguments for read-only deployment/pod metadata and reads the running image's
`/usr/share/knowl/revision`; it never reads tokens or environment secrets.

Prepare a JSON metadata file describing the exact version being tested:

```json
{
  "kind": "candidate",
  "targetBase": "https://lab.metalagman.dev/balda/knowl/",
  "sourceRevision": "<reviewed 40-character Knowl commit>",
  "image": "<Knowl image repository>@sha256:<immutable image digest>",
  "configurationRevision": "<reviewed Knowl configuration revision>",
  "adaptation": {
    "repositoryRevision": "<Balda repository commit>",
    "dirty": false,
    "files": {"docker/knowl-ui-prefix.sh": "<SHA-256 of actual build input>"}
  },
  "cluster": {
    "kubeconfig": "/home/bigboss/.kube/asus_kubeconfig",
    "namespace": "balda",
    "deployment": "balda-knowl",
    "container": "knowl"
  }
}
```

Replace placeholders with the reviewed build/plan records. Adaptation provenance
is declared build evidence, not an independently observed runtime fact. If its
working tree is dirty, record that fact and hashes of the actual inputs. Never
substitute a clean repository SHA for uncommitted build inputs.

```sh
KNOWL_E2E_METADATA=/tmp/knowl-balda-candidate.json \
KNOWL_BROWSER_ARTIFACT_DIR=/tmp/knowl-balda-candidate \
  node tools/webui-browser/balda.cjs
```

The runner verifies the actual ready pod image/digest, revision file and
configuration annotation against the expected record before browser actions and
again afterward. Runtime mismatch is a failure. `report.json` distinguishes
expected metadata from observed runtime, records URL/scenario/viewport/outcome,
failed reads, unavailable real-data cases, screenshot SHA-256 hashes, and the
main runner plus imported helper SHA-256 hashes. The published-structure
scenario records actual root group/page identities and checks immediate nesting
without requiring specific group names. Known page paths are reached through
actual group or index-page disclosure; no production fixtures are created. All
four required viewport pairs are used. The checks include the exact Auto mode
URL and Conversations context, direct/reload/Back/Forward and ancestor trimming,
actual parent title/caret independence, Balda releases without a caret, absence
of `All pages`, five-screen native chrome, keyboard/drawers, source/context
reading, and Search navigation. Search is submitted once; when embeddings are
enabled it uses the configured retrieval provider normally. Original/upstream
links are never opened.

Use `"kind": "baseline"` only to reproduce old deployed defects. Failures retain
nonzero exit status and their screenshots; baseline results never set
`candidateAccepted`. Deterministic fixtures supplement missing corner cases but
do not replace acceptance of the actual reviewed candidate running in Balda.
The Story remains open until that candidate's required live scenarios pass.

Keep failed historical acceptance reports separate from local fixtures and
diagnostics. The prior source7b79/image8125 full49 run had46PASS and three
responsive heading-focus failures; it remains failed evidence. A passing local
regression or a diagnostic against that older runtime cannot accept the revised
generic structure. Complete corrected-candidate evidence is produced only after
the reviewed source/image/configuration is actually deployed with authorization.
