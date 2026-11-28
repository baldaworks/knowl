package mcpsdk

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf8"

	knowlmcp "github.com/baldaworks/knowl/pkg/knowl/mcp"
	"gopkg.in/yaml.v3"
)

const (
	knowlSkillRoot               = "skills/knowl"
	knowlSkillFile               = "SKILL.md"
	knowlSkillURI                = "skill://knowl/SKILL.md"
	knowlToolContractFingerprint = "f4adec648a6e7449bda5c41363a0d6e9c7b11292c0ff2d8d16e4e75a58ccca5d"
	markdownMediaType            = "text/markdown"
)

//go:embed skills/knowl
var embeddedSkills embed.FS

type skillResource struct {
	URI       string
	MediaType string
	Size      int64
	SHA256    string
	Content   []byte
}

type skillCatalog struct {
	URI         string
	Frontmatter map[string]any
	Resource    skillResource
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func newEmbeddedSkillCatalog(tools []knowlmcp.Tool) (skillCatalog, error) {
	if err := validateToolContract(tools); err != nil {
		return skillCatalog{}, err
	}
	return loadSkillCatalog(embeddedSkills, knowlSkillRoot)
}

func loadSkillCatalog(files fs.FS, root string) (skillCatalog, error) {
	entries, err := fs.ReadDir(files, root)
	if err != nil {
		return skillCatalog{}, fmt.Errorf("read skill inventory: %w", err)
	}
	if len(entries) != 1 || entries[0].IsDir() || entries[0].Name() != knowlSkillFile {
		return skillCatalog{}, fmt.Errorf("knowl skill must contain exactly %s", knowlSkillFile)
	}
	content, err := fs.ReadFile(files, root+"/"+knowlSkillFile)
	if err != nil {
		return skillCatalog{}, fmt.Errorf("read Knowl skill: %w", err)
	}
	frontmatter, err := parseSkillFrontmatter(content)
	if err != nil {
		return skillCatalog{}, err
	}
	digest := sha256.Sum256(content)
	return skillCatalog{
		URI:         knowlSkillURI,
		Frontmatter: frontmatter,
		Resource: skillResource{
			URI:       knowlSkillURI,
			MediaType: markdownMediaType,
			Size:      int64(len(content)),
			SHA256:    hex.EncodeToString(digest[:]),
			Content:   bytes.Clone(content),
		},
	}, nil
}

func parseSkillFrontmatter(content []byte) (map[string]any, error) {
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("knowl skill is not valid UTF-8")
	}
	const opening = "---\n"
	if !bytes.HasPrefix(content, []byte(opening)) {
		return nil, fmt.Errorf("knowl skill frontmatter is missing")
	}
	remainder := content[len(opening):]
	end := bytes.Index(remainder, []byte("\n---\n"))
	if end < 0 {
		return nil, fmt.Errorf("knowl skill frontmatter is not terminated")
	}
	var required skillFrontmatter
	if err := yaml.Unmarshal(remainder[:end], &required); err != nil {
		return nil, fmt.Errorf("parse Knowl skill frontmatter: %w", err)
	}
	if required.Name != "knowl" || strings.TrimSpace(required.Description) == "" {
		return nil, fmt.Errorf("knowl skill frontmatter requires name knowl and a description")
	}
	var values map[string]any
	if err := yaml.Unmarshal(remainder[:end], &values); err != nil {
		return nil, fmt.Errorf("parse Knowl skill frontmatter fields: %w", err)
	}
	if len(values) != 2 {
		return nil, fmt.Errorf("knowl skill frontmatter may contain only name and description")
	}
	if _, ok := values["name"]; !ok {
		return nil, fmt.Errorf("knowl skill frontmatter name is missing")
	}
	if _, ok := values["description"]; !ok {
		return nil, fmt.Errorf("knowl skill frontmatter description is missing")
	}
	return values, nil
}

func validateToolContract(tools []knowlmcp.Tool) error {
	encoded, err := json.Marshal(tools)
	if err != nil {
		return fmt.Errorf("encode Knowl MCP tool contract: %w", err)
	}
	digest := sha256.Sum256(encoded)
	actual := hex.EncodeToString(digest[:])
	if actual != knowlToolContractFingerprint {
		return fmt.Errorf("knowl MCP tool contract changed: got %s, review the usage skill and update its fingerprint", actual)
	}
	return nil
}
