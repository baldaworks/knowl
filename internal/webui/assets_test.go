package webui

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const cssExtension = "css"

func TestOwnAssetsMatchShellAndContent(t *testing.T) {
	h, err := New(Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	urls := shellOwnAssetURLs(t, h)
	for _, extension := range []string{"js", cssExtension} {
		body, readErr := embedded.ReadFile("assets/ui." + extension)
		if readErr != nil {
			t.Fatal(readErr)
		}
		wantURL := fmt.Sprintf("/ui/assets/ui.%x.%s", sha256.Sum256(body), extension)
		if urls[extension] != wantURL {
			t.Fatalf("%s shell URL = %q, want %q", extension, urls[extension], wantURL)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(extension+"/"+method, func(t *testing.T) {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(method, wantURL, nil))
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d", w.Code)
				}
				contentType := "text/css; charset=utf-8"
				if extension == "js" {
					contentType = "text/javascript; charset=utf-8"
				}
				if w.Header().Get("Content-Type") != contentType || w.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
					t.Fatalf("content headers = %v", w.Header())
				}
				if w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != CSP {
					t.Fatalf("cache/security headers = %v", w.Header())
				}
				if method == http.MethodGet && !bytes.Equal(w.Body.Bytes(), body) {
					t.Fatal("served bytes differ from embedded bytes")
				}
				if method == http.MethodHead && w.Body.Len() != 0 {
					t.Fatal("HEAD returned a body")
				}
			})
		}
		stable := httptest.NewRecorder()
		h.ServeHTTP(stable, httptest.NewRequest(http.MethodGet, "/ui/assets/ui."+extension, nil))
		if stable.Code != http.StatusOK || stable.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(stable.Body.Bytes(), body) {
			t.Fatalf("stable %s: status=%d cache=%q", extension, stable.Code, stable.Header().Get("Cache-Control"))
		}
	}
	for _, route := range []string{
		"/ui/assets/ui." + strings.Repeat("0", 64) + ".js",
		"/ui/assets/ui." + strings.Repeat("0", 64) + ".css",
		"/ui/assets/ui.invalid.js", "/ui/assets/ui..css",
		"/ui/assets/", "/ui/assets/vendor/", "/ui/assets/../templates/shell.html",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", route, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, urls["js"], nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", w.Code)
	}
}

func TestOwnAssetChangedBytesChangeOnlyTheirURL(t *testing.T) {
	original, err := New(Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	originalURLs := shellOwnAssetURLs(t, original)
	for _, extension := range []string{"js", cssExtension} {
		t.Run(extension, func(t *testing.T) {
			files := ownAssetTestFiles(t)
			name := "assets/ui." + extension
			files[name].Data = append(files[name].Data, []byte("\n/* changed build */\n")...)
			updated, createErr := newHandler(files, Dependencies{})
			if createErr != nil {
				t.Fatal(createErr)
			}
			updatedURLs := shellOwnAssetURLs(t, updated)
			for _, other := range []string{"js", cssExtension} {
				if (updatedURLs[other] != originalURLs[other]) != (other == extension) {
					t.Fatalf("changed %s: old=%v new=%v", extension, originalURLs, updatedURLs)
				}
			}
			response := httptest.NewRecorder()
			updated.ServeHTTP(response, httptest.NewRequest(http.MethodGet, updatedURLs[extension], nil))
			if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), files[name].Data) {
				t.Fatal("changed asset did not serve its exact bytes")
			}
			old := httptest.NewRecorder()
			updated.ServeHTTP(old, httptest.NewRequest(http.MethodGet, originalURLs[extension], nil))
			if old.Code != http.StatusNotFound {
				t.Fatalf("old digest status = %d", old.Code)
			}
		})
	}
}

func TestMissingOwnAssetFailsConstruction(t *testing.T) {
	for _, extension := range []string{"js", cssExtension} {
		t.Run(extension, func(t *testing.T) {
			files := ownAssetTestFiles(t)
			delete(files, "assets/ui."+extension)
			if _, err := newHandler(files, Dependencies{}); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("constructor error = %v, want wrapped fs.ErrNotExist", err)
			}
		})
	}
}

func shellOwnAssetURLs(t *testing.T, h *Handler) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ui/knowledge", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("shell status=%d cache=%q", w.Code, w.Header().Get("Cache-Control"))
	}
	doc, err := html.Parse(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]string{}
	walk(doc, func(n *html.Node) {
		var target string
		switch n.DataAtom {
		case atom.Script:
			target = attribute(n, "src")
		case atom.Link:
			target = attribute(n, "href")
		}
		if !strings.HasPrefix(target, "/ui/assets/ui.") {
			return
		}
		extension := strings.TrimPrefix(path.Ext(target), ".")
		if urls[extension] != "" {
			t.Fatalf("duplicate own asset %s", extension)
		}
		urls[extension] = target
	})
	if len(urls) != 2 || urls["js"] == "" || urls[cssExtension] == "" {
		t.Fatalf("shell own assets = %v", urls)
	}
	return urls
}

func ownAssetTestFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	if err := fs.WalkDir(embedded, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := embedded.ReadFile(name)
		if err != nil {
			return err
		}
		files[name] = &fstest.MapFile{Data: data}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
