package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

const (
	adminLTEPackage  = "admin-lte"
	bootstrapPackage = "bootstrap"
	htmxPackage      = "htmx.org"
)

var ErrAssetsInvalid = errors.New("invalid embedded vendor assets")

type vendorEntry struct {
	File    string `json:"file"`
	Package string `json:"package"`
	Version string `json:"version"`
	Source  string `json:"source"`
	Member  string `json:"member"`
	SHA256  string `json:"sha256"`
}

func validateAssets(files fs.FS) error {
	data, err := fs.ReadFile(files, "assets/vendor/manifest.json")
	if err != nil {
		return fmt.Errorf("read vendor manifest: %w", err)
	}
	var entries []vendorEntry
	if err = json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("decode vendor manifest: %w", err)
	}
	expected := map[string]string{"adminlte.min.css": adminLTEPackage, "adminlte.min.js": adminLTEPackage, "admin-lte-LICENSE.txt": adminLTEPackage, "bootstrap.min.css": bootstrapPackage, "bootstrap.bundle.min.js": bootstrapPackage, "bootstrap-LICENSE.txt": bootstrapPackage, "htmx.min.js": htmxPackage, "htmx-org-LICENSE.txt": htmxPackage}
	versions := map[string]string{adminLTEPackage: "4.10.0", bootstrapPackage: "5.3.8", htmxPackage: "2.0.11"}
	if len(entries) != len(expected) {
		return ErrAssetsInvalid
	}
	for _, entry := range entries {
		if expected[entry.File] != entry.Package || entry.Package == "" || versions[entry.Package] != entry.Version || !strings.HasPrefix(entry.Source, "https://registry.npmjs.org/") || !strings.HasPrefix(entry.Member, "package/") {
			return ErrAssetsInvalid
		}
		data, readErr := fs.ReadFile(files, "assets/vendor/"+entry.File)
		if readErr != nil {
			return fmt.Errorf("read vendor asset: %w", readErr)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			return ErrAssetsInvalid
		}
		delete(expected, entry.File)
	}
	if len(expected) != 0 {
		return ErrAssetsInvalid
	}
	members, err := fs.ReadDir(files, "assets/vendor")
	if err != nil {
		return fmt.Errorf("read vendor members: %w", err)
	}
	if len(members) != len(entries)+1 {
		return ErrAssetsInvalid
	}
	for _, member := range members {
		if member.IsDir() {
			return ErrAssetsInvalid
		}
	}
	return nil
}
