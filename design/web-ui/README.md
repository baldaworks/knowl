# Knowl UI historical design reference

Static HTML reference approved for Story `knowl-d8b` before implementation.
This historical prototype records that design review; the running UI has since
changed. Use the [web UI guide](../../docs/web-ui.md) for current setup,
navigation, and behavior. All documents, revisions, search results, and
operation states here are demonstration fixtures. This directory does not
implement or call the Knowl API.

The reference uses HTMX 2.0.11, Bootstrap 5.3.8 and AdminLTE 4.10.0.
Libraries and their licenses are saved locally in `vendor/`; `manifest.json`
records the exact source archive, member and SHA-256 for each file.

Open `index.html` directly to browse the reference, or serve this directory for
HTMX navigation and HTML fragment loading:

```sh
python3 -m http.server 8093 --bind 127.0.0.1 --directory design/web-ui
```

Open `http://localhost:8093/`. The four screens are Knowledge, Search,
Operations and Sources. Search returns fixed sample evidence after submission;
saved-source buttons open sample immutable text. Operation filters and cards
have local demo behavior. Related-page placeholders are explicitly marked.

The footer's **Reference state** selector exposes empty knowledge, a missing
saved revision, degraded search and unavailable retrieval diagnostics. Wiki
states apply on Knowledge; search states apply on Search.

The implemented Go service includes authentication, live retrieval, canonical
reads, and selected-operation polling. Those runtime features are not provided
by this prototype. It contains no real operator token or production workspace
data.

## Brand asset

The user-supplied original owl logo is saved unchanged in
`assets/knowl-logo.png`. The shared sidebar displays its owl mark through CSS
clipping alongside the Knowl name. The original image includes the full wordmark
and remains available for reuse.
