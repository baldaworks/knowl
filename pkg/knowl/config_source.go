package knowl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
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
	defaultGitTransferBytes   = int64(500 << 20)
	defaultGitCacheBytes      = int64(512 << 20)
	maximumGitResourceBytes   = int64(8 << 30)
)

var (
	gitSCPPattern        = regexp.MustCompile(`^[a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+:[a-zA-Z0-9_./~-]+$`)
	gitCommitSHAPattern  = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	envVarPattern        = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	gitRefInvalidPattern = regexp.MustCompile(`[\s\x00-\x1f\x7f~^:?*\[\\@{]|//|\.\.|\.lock$`)
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
		switch source.Type {
		case domain.SourceTypeFilesystem:
			if source.Config.Filesystem == nil || source.Config.Git != nil {
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
			source.Config.Filesystem = &filesystem
		case domain.SourceTypeGit:
			if source.Config.Git == nil || source.Config.Filesystem != nil {
				return nil, fmt.Errorf("source %q must use the supported git config", source.ID)
			}
			git, err := normalizeGitSource(source.ID, baseDir, *source.Config.Git)
			if err != nil {
				return nil, err
			}
			source.Config.Git = git
		default:
			return nil, fmt.Errorf("source %q has unsupported type %q", source.ID, source.Type)
		}
		if err := normalizeSyncPolicy(&source.Sync); err != nil {
			return nil, fmt.Errorf("source %q sync: %w", source.ID, err)
		}
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

func normalizeGitSource(sourceID domain.SourceID, baseDir string, raw domain.GitSourceConfig) (*domain.GitSourceConfig, error) {
	git := raw
	remote := strings.TrimSpace(git.Remote)
	if remote == "" {
		return nil, fmt.Errorf("source %q git remote is required", sourceID)
	}
	canonicalRemote, err := validateAndCanonicalizeGitRemote(remote)
	if err != nil {
		return nil, fmt.Errorf("source %q remote: %w", sourceID, err)
	}
	git.Remote = canonicalRemote

	ref := strings.TrimSpace(git.Ref)
	if ref == "" {
		return nil, fmt.Errorf("source %q git ref is required", sourceID)
	}
	normalizedRef, kind, err := validateAndNormalizeGitRef(ref, git.RefKind)
	if err != nil {
		return nil, fmt.Errorf("source %q ref: %w", sourceID, err)
	}
	git.Ref = normalizedRef
	git.RefKind = kind

	git.Include, err = normalizeInclude(git.Include)
	if err != nil {
		return nil, fmt.Errorf("source %q include: %w", sourceID, err)
	}
	git.Flavor = strings.ToLower(strings.TrimSpace(git.Flavor))
	if git.Flavor == "" {
		git.Flavor = domain.SourceFlavorMarkdown
	}
	if git.Flavor != domain.SourceFlavorMarkdown && git.Flavor != domain.SourceFlavorObsidian && git.Flavor != domain.SourceFlavorOKF {
		return nil, fmt.Errorf("source %q has unsupported flavor %q", sourceID, git.Flavor)
	}
	git.URIBase, err = normalizeURIBase(git.URIBase)
	if err != nil {
		return nil, fmt.Errorf("source %q uri_base: %w", sourceID, err)
	}
	git.Auth, err = normalizeGitAuth(baseDir, git.Auth)
	if err != nil {
		return nil, fmt.Errorf("source %q auth: %w", sourceID, err)
	}
	git.KnownHosts = normalizeKnownHosts(git.KnownHosts)
	if strings.HasPrefix(git.Remote, "ssh://") || gitSCPPattern.MatchString(git.Remote) {
		if (git.Auth.SecretEnv == "" && git.Auth.KeyFile == "") || len(git.KnownHosts) == 0 {
			return nil, fmt.Errorf("source %q SSH remote requires external key authentication and known_hosts trust", sourceID)
		}
	}
	if git.MaxTransferBytes == 0 {
		git.MaxTransferBytes = defaultGitTransferBytes
	}
	if git.MaxCacheBytes == 0 {
		git.MaxCacheBytes = defaultGitCacheBytes
	}
	if git.MaxTransferBytes < 0 || git.MaxTransferBytes > maximumGitResourceBytes {
		return nil, fmt.Errorf("source %q max_transfer_bytes must be between 1 and %d", sourceID, maximumGitResourceBytes)
	}
	if git.MaxCacheBytes < 0 || git.MaxCacheBytes > maximumGitResourceBytes {
		return nil, fmt.Errorf("source %q max_cache_bytes must be between 1 and %d", sourceID, maximumGitResourceBytes)
	}
	repositoryIdentity := strings.TrimSpace(git.RepositoryID)
	if repositoryIdentity == "" {
		repositoryIdentity = canonicalRemote
	}
	digest := sha256.Sum256([]byte(repositoryIdentity))
	git.RepositoryID = hex.EncodeToString(digest[:])
	return &git, nil
}

func validateAndCanonicalizeGitRemote(remote string) (string, error) {
	if strings.HasPrefix(remote, "file:") || strings.HasPrefix(remote, "/") || strings.HasPrefix(remote, "./") || strings.HasPrefix(remote, "../") || strings.HasPrefix(remote, "~") {
		return "", fmt.Errorf("local filesystem paths and file transport are not permitted")
	}
	if strings.HasPrefix(remote, "ext::") || strings.Contains(remote, "--") {
		return "", fmt.Errorf("transport helper and command options are not permitted")
	}
	if strings.HasPrefix(remote, "http://") {
		return "", fmt.Errorf("unencrypted HTTP transport is not permitted; use HTTPS or SSH")
	}
	if strings.HasPrefix(remote, "https://") {
		parsed, err := url.Parse(remote)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("invalid HTTPS remote URL")
		}
		if parsed.User != nil {
			return "", fmt.Errorf("credentials are not permitted in HTTPS remote URL")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("query parameters and fragments are not permitted in remote URL")
		}
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if strings.HasPrefix(remote, "ssh://") {
		parsed, err := url.Parse(remote)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("invalid SSH remote URL")
		}
		if parsed.User != nil {
			if _, present := parsed.User.Password(); present {
				return "", fmt.Errorf("credentials are not permitted in SSH remote URL")
			}
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("query parameters and fragments are not permitted in remote URL")
		}
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if gitSCPPattern.MatchString(remote) {
		return remote, nil
	}
	return "", fmt.Errorf("unsupported remote transport scheme; only HTTPS and SSH are supported")
}

func validateAndNormalizeGitRef(ref, refKind string) (string, string, error) {
	if gitCommitSHAPattern.MatchString(ref) {
		return "", "", fmt.Errorf("arbitrary commit hashes are not supported as tracked ref")
	}
	if gitRefInvalidPattern.MatchString(ref) || strings.Contains(ref, "/.") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasPrefix(ref, ".") {
		return "", "", fmt.Errorf("invalid git ref %q", ref)
	}
	refKind = strings.ToLower(strings.TrimSpace(refKind))
	if refKind != "" && refKind != domain.GitRefKindBranch && refKind != domain.GitRefKindTag {
		return "", "", fmt.Errorf("ref_kind %q must be \"branch\" or \"tag\"", refKind)
	}
	if strings.HasPrefix(ref, "refs/heads/") {
		if refKind != "" && refKind != domain.GitRefKindBranch {
			return "", "", fmt.Errorf("ref %q conflicts with ref_kind %q", ref, refKind)
		}
		return ref, domain.GitRefKindBranch, nil
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		if refKind != "" && refKind != domain.GitRefKindTag {
			return "", "", fmt.Errorf("ref %q conflicts with ref_kind %q", ref, refKind)
		}
		return ref, domain.GitRefKindTag, nil
	}
	if refKind == domain.GitRefKindTag {
		return "refs/tags/" + ref, domain.GitRefKindTag, nil
	}
	return "refs/heads/" + ref, domain.GitRefKindBranch, nil
}

func normalizeGitAuth(baseDir string, auth domain.GitAuthConfig) (domain.GitAuthConfig, error) {
	auth.SecretEnv = strings.TrimSpace(auth.SecretEnv)
	if auth.SecretEnv != "" && !envVarPattern.MatchString(auth.SecretEnv) {
		return domain.GitAuthConfig{}, fmt.Errorf("secret_env %q is not a valid environment variable name", auth.SecretEnv)
	}
	auth.KeyFile = strings.TrimSpace(auth.KeyFile)
	if auth.KeyFile != "" {
		if !filepath.IsAbs(auth.KeyFile) {
			auth.KeyFile = filepath.Join(baseDir, auth.KeyFile)
		}
		keyPath, err := canonicalPath(auth.KeyFile)
		if err != nil {
			return domain.GitAuthConfig{}, fmt.Errorf("key_file %q: %w", auth.KeyFile, err)
		}
		auth.KeyFile = keyPath
	}
	return auth, nil
}

func normalizeKnownHosts(hosts []string) []string {
	if len(hosts) == 0 {
		return nil
	}
	unique := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host != "" {
			unique[host] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(unique))
	for host := range unique {
		normalized = append(normalized, host)
	}
	sort.Strings(normalized)
	return normalized
}
