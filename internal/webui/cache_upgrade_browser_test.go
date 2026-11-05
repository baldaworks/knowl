//go:build browser

package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const cacheQAPath = "/__qa/cache"
const cacheBaldaIndex = "catalogs/balda/index"

type upgradeAsset struct {
	Body   []byte
	Header http.Header
	Hits   int
	Misses int
}

type upgradeCache struct {
	mu        sync.Mutex
	candidate bool
	ui        *Handler
	legacy    *Handler
	assets    map[string]*upgradeAsset
	expected  map[string]string
}

// TestBrowserCacheUpgrade warms actual historical own assets, then changes only
// the origin build behind the same cache and browser. The red control uses the
// exact pre-fix shell with stable asset references through the production handler.
func TestBrowserCacheUpgrade(t *testing.T) {
	for _, red := range []bool{true, false} {
		name := "candidate"
		if red {
			name = "stable-shell-red"
		}
		t.Run(name, func(t *testing.T) {
			ui := wikiLayoutBrowserUI(t, func(root string) {
				for relative, body := range map[string]string{
					cacheBaldaIndex + ".md":                 "# Balda\n\n[Conversations](conversations/index.md)\n\n[Releases](releases/index.md)\n",
					"catalogs/balda/conversations/index.md": "# Conversations\n\n[Auto mode](../../../concepts/auto-mode.md)\n",
					"catalogs/balda/releases/index.md":      "# Balda releases\n\nPublished release information.\n",
					"concepts/auto-mode.md":                 "---\ntype: concept\ntitle: Auto mode\n---\n# Auto mode\n\nActual auto mode information.\n",
				} {
					writeWikiBrowserPage(t, root, relative, []byte(body))
				}
			})
			files := ownAssetTestFiles(t)
			files["templates/shell.html"].Data = historicalCacheFixture(t, "stable-shell.html")
			for _, extension := range []string{"js", cssExtension} {
				files["assets/ui."+extension].Data = historicalCacheFixture(t, "legacy-ui."+extension)
			}
			legacy, err := newHandler(files, ui.dependencies)
			if err != nil {
				t.Fatal(err)
			}
			if red {
				redFiles := ownAssetTestFiles(t)
				redFiles["templates/shell.html"].Data = historicalCacheFixture(t, "stable-shell.html")
				ui, err = newHandler(redFiles, ui.dependencies)
				if err != nil {
					t.Fatal(err)
				}
			}
			cache := &upgradeCache{ui: ui, legacy: legacy, assets: map[string]*upgradeAsset{}, expected: map[string]string{}}
			shell := httptest.NewRecorder()
			ui.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/ui/knowledge", nil))
			cache.expected["html"] = fmt.Sprintf("%x", sha256.Sum256(shell.Body.Bytes()))
			for _, extension := range []string{"js", cssExtension} {
				data, readErr := embedded.ReadFile("assets/ui." + extension)
				if readErr != nil {
					t.Fatal(readErr)
				}
				cache.expected[extension] = fmt.Sprintf("%x", sha256.Sum256(data))
			}
			host := httptest.NewServer(cache)
			defer host.Close()
			script, err := filepath.Abs("../../tools/webui-browser/cache_upgrade.cjs")
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			if configured := os.Getenv("KNOWL_BROWSER_ARTIFACT_DIR"); configured != "" {
				directory = filepath.Join(configured, name)
			}
			command := exec.CommandContext(t.Context(), "node", script)
			command.Env = append(os.Environ(), "KNOWL_BROWSER_URL="+host.URL, "KNOWL_BROWSER_ARTIFACT_DIR="+directory)
			output, runErr := command.CombinedOutput()
			reportBytes, readErr := os.ReadFile(filepath.Join(directory, "cache-upgrade.json"))
			if readErr != nil {
				t.Fatalf("report: %v; browser: %v\n%s", readErr, runErr, output)
			}
			var result struct {
				Outcome     string `json:"outcome"`
				FailureCode string `json:"failureCode"`
			}
			if err = json.Unmarshal(reportBytes, &result); err != nil {
				t.Fatal(err)
			}
			if red {
				if runErr == nil || result.FailureCode != "asset_identity_mismatch" {
					t.Fatalf("red control did not reject stale bytes: %+v; err=%v\n%s", result, runErr, output)
				}
				t.Log("RED reproduced: actual pre-fix shell loads cached historical bytes; report=" + directory)
			} else if runErr != nil || result.Outcome != "pass" {
				t.Fatalf("browser: %v; %+v\n%s", runErr, result, output)
			}
			t.Log(string(output))
		})
	}
}

func historicalCacheFixture(t *testing.T, name string) []byte {
	t.Helper()
	provenance, err := os.ReadFile("testdata/cache-upgrade/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]struct {
		SHA256   string `json:"sha256"`
		Revision string `json:"revision"`
		Path     string `json:"path"`
	}
	if err = json.Unmarshal(provenance, &manifest); err != nil {
		t.Fatal(err)
	}
	compressed, err := os.ReadFile("testdata/cache-upgrade/" + name + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(reader, 256<<10))
	if err != nil {
		t.Fatal(err)
	}
	record := manifest[name]
	if record.Revision == "" || record.Path == "" || fmt.Sprintf("%x", sha256.Sum256(body)) != record.SHA256 {
		t.Fatalf("historical provenance mismatch: %s", name)
	}
	// Compressed canonical inputs remain unchanged by the existing local prefix
	// adaptation; adapt the decoded fixture exactly as that build adapts its UI.
	return bytes.ReplaceAll(body, []byte("/"+"ui/"), []byte("/ui/"))
}

func (c *upgradeCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.URL.Path == cacheQAPath {
		if r.Method == http.MethodPost {
			c.candidate = true
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		statistics := map[string]any{}
		for name, asset := range c.assets {
			statistics[name] = map[string]any{"hits": asset.Hits, "misses": asset.Misses, "sha256": fmt.Sprintf("%x", sha256.Sum256(asset.Body))}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidate": c.candidate, "expected": c.expected, "assets": statistics})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/ui/assets/ui.") {
		if asset := c.assets[r.URL.Path]; asset != nil {
			asset.Hits++
			for name, values := range asset.Header {
				w.Header()[name] = append([]string(nil), values...)
			}
			w.Header().Set("X-QA-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(asset.Body)
			return
		}
		response := httptest.NewRecorder()
		origin := c.legacy
		if c.candidate {
			origin = c.ui
		}
		origin.ServeHTTP(response, r)
		if !c.candidate && response.Code == http.StatusOK {
			response.Header().Set("Cache-Control", "public, max-age=14400")
		}
		if response.Code == http.StatusOK {
			asset := &upgradeAsset{Body: bytes.Clone(response.Body.Bytes()), Header: response.Header().Clone(), Misses: 1}
			c.assets[r.URL.Path] = asset
		}
		for name, values := range response.Header() {
			w.Header()[name] = append([]string(nil), values...)
		}
		w.Header().Set("X-QA-Cache", "MISS")
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
		return
	}
	ui := c.legacy
	if c.candidate {
		ui = c.ui
	}
	if strings.HasPrefix(r.URL.Path, "/ui/fragments/") {
		if r.Header.Get("Authorization") != "Bearer "+wikiBrowserToken {
			ui.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ui.Fragments(w, r)
		return
	}
	ui.ServeHTTP(w, r)
}
