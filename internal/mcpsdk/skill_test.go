package mcpsdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	knowlmcp "github.com/baldaworks/knowl/pkg/knowl/mcp"
)

const testSkillPath = "knowl/SKILL.md"

func TestEmbeddedSkillMatchesCanonicalGitFile(t *testing.T) {
	catalog, err := loadSkillCatalog(embeddedSkills, knowlSkillRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	canonical, err := os.ReadFile(filepath.Join(repositoryRoot, "internal/mcpsdk/skills/knowl/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(catalog.Resource.Content) != string(canonical) {
		t.Fatal("embedded skill differs from canonical Git file")
	}
	digest := sha256.Sum256(canonical)
	if catalog.URI != knowlSkillURI || catalog.Resource.URI != knowlSkillURI ||
		catalog.Resource.MediaType != markdownMediaType || catalog.Resource.Size != int64(len(canonical)) ||
		catalog.Resource.SHA256 != hex.EncodeToString(digest[:]) || catalog.Frontmatter["name"] != "knowl" {
		t.Fatalf("catalog = %#v", catalog)
	}

	manifest, err := os.ReadFile(filepath.Join(repositoryRoot, "skills.sh.json"))
	if err != nil {
		t.Fatal(err)
	}
	var discovery struct {
		Schema string   `json:"$schema"`
		Paths  []string `json:"paths"`
	}
	if err := json.Unmarshal(manifest, &discovery); err != nil {
		t.Fatal(err)
	}
	if discovery.Schema != "https://skills.sh/schemas/skills.sh.schema.json" || len(discovery.Paths) != 1 || discovery.Paths[0] != "./internal/mcpsdk/skills" {
		t.Fatalf("skills.sh.json = %#v", discovery)
	}
	entries, err := os.ReadDir(filepath.Join(repositoryRoot, discovery.Paths[0], "knowl"))
	if err != nil || len(entries) != 1 || entries[0].Name() != knowlSkillFile || entries[0].IsDir() {
		t.Fatalf("discovered skill inventory = %#v, %v", entries, err)
	}
}

func TestSkillCatalogRejectsInvalidInventoryAndFrontmatter(t *testing.T) {
	valid := []byte("---\nname: knowl\ndescription: Use Knowl.\n---\n\n# Knowl\n")
	tests := []struct {
		name  string
		files fstest.MapFS
	}{
		{name: "extra resource", files: fstest.MapFS{
			testSkillPath:    &fstest.MapFile{Data: valid},
			"knowl/extra.md": &fstest.MapFile{Data: []byte("extra")},
		}},
		{name: "missing frontmatter", files: fstest.MapFS{
			testSkillPath: &fstest.MapFile{Data: []byte("# Knowl\n")},
		}},
		{name: "wrong name", files: fstest.MapFS{
			testSkillPath: &fstest.MapFile{Data: []byte("---\nname: other\ndescription: Use Knowl.\n---\n")},
		}},
		{name: "executable frontmatter", files: fstest.MapFS{
			testSkillPath: &fstest.MapFile{Data: []byte("---\nname: knowl\ndescription: Use Knowl.\nhooks: run-me\n---\n")},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadSkillCatalog(test.files, "knowl"); err == nil {
				t.Fatal("invalid skill accepted")
			}
		})
	}
}

func TestToolContractFingerprintRejectsDrift(t *testing.T) {
	if err := validateToolContract([]knowlmcp.Tool{{Name: "changed"}}); err == nil {
		t.Fatal("changed tool contract accepted")
	}
}

func TestSkillsExtensionRejectsInvalidCatalog(t *testing.T) {
	catalog, err := loadSkillCatalog(embeddedSkills, knowlSkillRoot)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*skillCatalog)
	}{
		{name: "wrong URI", mutate: func(c *skillCatalog) { c.URI = "skill://other/SKILL.md" }},
		{name: "wrong resource URI", mutate: func(c *skillCatalog) { c.Resource.URI = "skill://other/SKILL.md" }},
		{name: "wrong digest", mutate: func(c *skillCatalog) { c.Resource.SHA256 = "invalid" }},
		{name: "wrong size", mutate: func(c *skillCatalog) { c.Resource.Size++ }},
		{name: "wrong media type", mutate: func(c *skillCatalog) { c.Resource.MediaType = "text/plain" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := catalog
			test.mutate(&invalid)
			if err := validateSkillCatalogForExtension(invalid); err == nil {
				t.Fatal("invalid extension catalog accepted")
			}
		})
	}
	if err := attachSkillsExtension(nil, catalog); err == nil {
		t.Fatal("nil MCP server accepted")
	}
}
