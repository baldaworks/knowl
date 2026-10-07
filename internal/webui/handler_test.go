package webui

import (
	"bytes"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"golang.org/x/net/html"
)

func TestShellRoutesAndFiniteAssets(t *testing.T) {
	h, err := New(Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/ui/", "/ui/knowledge", "/ui/search", "/ui/operations", "/ui/sources"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, route, nil))
		if r.Code != 200 {
			t.Fatalf("%s status %d", route, r.Code)
		}
		doc, parseErr := html.Parse(r.Body)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		var logo, favicon, connect, navigation int
		var guestMenu, guestToggle bool
		walk(doc, func(n *html.Node) {
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[a.Key] = a.Val
			}
			if attrs["id"] == "workspace-navigation" {
				_, hidden := attrs["hidden"]
				_, inert := attrs["inert"]
				guestMenu = hidden && inert && attrs["aria-hidden"] == "true"
			}
			if attrs["id"] == "navigation-toggle" {
				_, guestToggle = attrs["hidden"]
			}

			for _, a := range n.Attr {
				if a.Key == "src" && a.Val == "/ui/assets/knowl-logo.png" {
					logo++
				}
				if a.Key == "id" && a.Val == "connect-form" {
					connect++
				}
				if a.Key == "data-screen" {
					navigation++
				}
			}
			if n.Type == html.ElementNode && n.Data == "link" && attrs["rel"] == "icon" && attrs["type"] == "image/png" && attrs["href"] == "/ui/assets/favicon.png" {
				favicon++
			}
		})
		if logo != 2 || favicon != 1 || connect != 1 || navigation != 4 || !guestMenu || !guestToggle {
			t.Fatalf("%s logo=%d favicon=%d connect=%d navigation=%d", route, logo, favicon, connect, navigation)
		}
	}
	icon := httptest.NewRecorder()
	h.ServeHTTP(icon, httptest.NewRequest(http.MethodGet, "/ui/assets/favicon.png", nil))
	if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("favicon response: status=%d content-type=%q", icon.Code, icon.Header().Get("Content-Type"))
	}
	if _, err := png.DecodeConfig(bytes.NewReader(icon.Body.Bytes())); err != nil {
		t.Fatalf("favicon is not a PNG: %v", err)
	}
	for _, route := range []string{"/ui/unknown", "/ui/assets/", "/ui/assets/missing.js", "/ui/assets/../templates/shell.html", "/v1/unknown"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, route, nil))
		if r.Code != 404 {
			t.Fatalf("%s status %d", route, r.Code)
		}
	}
}
func walk(n *html.Node, visit func(*html.Node)) {
	visit(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, visit)
	}
}

func TestInvalidEmbeddedInputsFailConstruction(t *testing.T) {
	for _, change := range []string{"template", "missing-template", "asset", "extra", "missing", "manifest"} {
		t.Run(change, func(t *testing.T) {
			files := fstest.MapFS{}
			err := fs.WalkDir(embedded, ".", func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					data, readErr := embedded.ReadFile(path)
					if readErr != nil {
						return readErr
					}
					files[path] = &fstest.MapFile{Data: data}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "template":
				files["templates/shell.html"] = &fstest.MapFile{Data: []byte("{{")}
			case "missing-template":
				files["templates/shell.html"] = &fstest.MapFile{Data: []byte("{{define \"other\"}}x{{end}}")}
			case "asset":
				files["assets/vendor/htmx.min.js"] = &fstest.MapFile{Data: []byte("tampered")}
			case "extra":
				files["assets/vendor/extra.js"] = &fstest.MapFile{Data: []byte("extra")}
			case "missing":
				delete(files, "assets/vendor/htmx.min.js")
			case "manifest":
				files["assets/vendor/manifest.json"] = &fstest.MapFile{Data: []byte("[]")}
			}
			if _, err := newHandler(files, Dependencies{}); err == nil {
				t.Fatal("invalid embedded input accepted")
			}
		})
	}
}
