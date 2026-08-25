package knowl

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	defaultSourceInclude      = "**/*.md"
	defaultSourceRetryInitial = time.Second
	defaultSourceRetryMaximum = time.Minute
	maximumSourceInterval     = 24 * time.Hour
	maximumSourceRetry        = time.Hour
	maximumSources            = 128
	maximumSourceIncludes     = 128
	maximumIncludePattern     = 1024
)

// NormalizeSources validates and canonicalizes configured sources without
// opening runtime resources. Relative roots are resolved from baseDir, or the
// process working directory when baseDir is empty.
func NormalizeSources(workspace, baseDir string, sources []domain.Source) ([]domain.Source, error) {
	return normalizeSources(workspace, baseDir, sources)
}

func normalizeSources(workspace, baseDir string, sources []domain.Source) ([]domain.Source, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	if len(sources) > maximumSources {
		return nil, fmt.Errorf("configured sources exceed limit %d", maximumSources)
	}
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve source base directory: %w", err)
		}
	}
	workspacePath, err := canonicalPath(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace for sources: %w", err)
	}

	normalized := make([]domain.Source, 0, len(sources))
	seen := make(map[domain.SourceID]struct{}, len(sources))
	for _, source := range sources {
		if err := app.ValidateSourceID(source.ID); err != nil {
			return nil, fmt.Errorf("source %q has an invalid id: %w", source.ID, err)
		}
		if _, exists := seen[source.ID]; exists {
			return nil, fmt.Errorf("source id %q is duplicated", source.ID)
		}
		seen[source.ID] = struct{}{}
		if source.Type != domain.SourceTypeFilesystem || source.Config.Filesystem == nil {
			return nil, fmt.Errorf("source %q must use the supported filesystem config", source.ID)
		}

		filesystem := *source.Config.Filesystem
		root := strings.TrimSpace(filesystem.Root)
		if root == "" {
			return nil, fmt.Errorf("source %q filesystem root is required", source.ID)
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(baseDir, root)
		}
		root, err = canonicalPath(root)
		if err != nil {
			return nil, fmt.Errorf("resolve source %q root: %w", source.ID, err)
		}
		if pathsOverlap(workspacePath, root) {
			return nil, fmt.Errorf("source %q root %q overlaps workspace %q", source.ID, root, workspacePath)
		}
		filesystem.Root = root
		filesystem.Include, err = normalizeInclude(filesystem.Include)
		if err != nil {
			return nil, fmt.Errorf("source %q include: %w", source.ID, err)
		}
		filesystem.Flavor = strings.ToLower(strings.TrimSpace(filesystem.Flavor))
		if filesystem.Flavor == "" {
			filesystem.Flavor = domain.SourceFlavorMarkdown
		}
		if filesystem.Flavor != domain.SourceFlavorMarkdown && filesystem.Flavor != domain.SourceFlavorObsidian && filesystem.Flavor != domain.SourceFlavorOKF {
			return nil, fmt.Errorf("source %q has unsupported flavor %q", source.ID, filesystem.Flavor)
		}
		filesystem.URIBase, err = normalizeURIBase(filesystem.URIBase)
		if err != nil {
			return nil, fmt.Errorf("source %q uri_base: %w", source.ID, err)
		}
		if err := normalizeSyncPolicy(&source.Sync); err != nil {
			return nil, fmt.Errorf("source %q sync: %w", source.ID, err)
		}
		source.Config.Filesystem = &filesystem
		source.ConfigDigest, err = app.SourceConfigDigest(source)
		if err != nil {
			return nil, fmt.Errorf("digest source %q config: %w", source.ID, err)
		}
		normalized = append(normalized, source)
	}
	return normalized, nil
}

func normalizeInclude(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return []string{defaultSourceInclude}, nil
	}
	if len(patterns) > maximumSourceIncludes {
		return nil, fmt.Errorf("patterns exceed limit %d", maximumSourceIncludes)
	}
	unique := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || len(pattern) > maximumIncludePattern || strings.Contains(pattern, "\\") || strings.HasPrefix(pattern, "/") || path.Clean(pattern) != pattern {
			return nil, fmt.Errorf("pattern %q must be a clean relative slash path", pattern)
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == ".." {
				return nil, fmt.Errorf("pattern %q contains traversal", pattern)
			}
		}
		if _, err := path.Match(pattern, "validation.md"); err != nil {
			return nil, fmt.Errorf("pattern %q is invalid: %w", pattern, err)
		}
		unique[pattern] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for pattern := range unique {
		normalized = append(normalized, pattern)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeURIBase(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("must be an absolute URI with a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("scheme %q is not supported", parsed.Scheme)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("credentials, query, and fragment are not allowed")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func normalizeSyncPolicy(policy *domain.SourceSyncPolicy) error {
	if policy.Interval < 0 || policy.Interval > maximumSourceInterval {
		return fmt.Errorf("interval must be between zero and %s", maximumSourceInterval)
	}
	if policy.RetryInitial < 0 || policy.RetryInitial > maximumSourceRetry {
		return fmt.Errorf("retry_initial must be between zero and %s", maximumSourceRetry)
	}
	if policy.RetryMaximum < 0 || policy.RetryMaximum > maximumSourceRetry {
		return fmt.Errorf("retry_maximum must be between zero and %s", maximumSourceRetry)
	}
	if policy.RetryInitial == 0 {
		policy.RetryInitial = defaultSourceRetryInitial
	}
	if policy.RetryMaximum == 0 {
		policy.RetryMaximum = defaultSourceRetryMaximum
	}
	if policy.RetryMaximum < policy.RetryInitial {
		return fmt.Errorf("retry_maximum must not be less than retry_initial")
	}
	return nil
}

func canonicalPath(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	prefix := abs
	var suffix []string
	for {
		_, statErr := os.Lstat(prefix)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(prefix)
			if evalErr != nil {
				return "", evalErr
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return abs, nil
		}
		suffix = append(suffix, filepath.Base(prefix))
		prefix = parent
	}
}

func pathsOverlap(left, right string) bool {
	relative, err := filepath.Rel(left, right)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
