Development-only browser checks for the embedded UI:

```sh
npm ci --prefix tools/webui-browser
npx --prefix tools/webui-browser playwright install chromium
go test -tags browser ./internal/webui -run TestBrowserSecurity -count=1
```

The tagged Go test launches an isolated HTTP fixture using the production shell,
Markdown renderer and authentication/read boundary, then exercises it in Chromium.
Production Go builds and ordinary tests do not require Node. The test checks the
persisted `pageshow` cleanup handler explicitly; it does not assume that Chromium
will place a page into its back/forward cache during automation.
