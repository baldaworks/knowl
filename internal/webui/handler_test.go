package webui

import (
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
		var logo, connect, navigation int
		walk(doc, func(n *html.Node) {
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
		})
		if logo != 1 || connect != 1 || navigation != 4 {
			t.Fatalf("%s logo=%d connect=%d navigation=%d", route, logo, connect, navigation)
		}
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
