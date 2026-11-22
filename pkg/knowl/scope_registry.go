package knowl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const DefaultPublicScope domain.ScopeName = "default"

var ErrScopeConfigInvalid = errors.New("invalid scope configuration")

// ScopeConfig declares one configured public scope before trusted
// normalization.
type ScopeConfig struct {
	Name     domain.ScopeName
	Ref      domain.ScopeRef
	Disabled bool
	Sources  []domain.Source
}

// ResolveAuthorizedScope normalizes configuration, resolves one public scope
// name (blank means default), and authorizes it before returning durable
// context to a trusted in-process adapter such as the CLI.
func (config Config) ResolveAuthorizedScope(ctx context.Context, name domain.ScopeName, operation app.OperationKind) (ScopeContext, error) {
	normalized, err := config.normalized()
	if err != nil {
		return ScopeContext{}, err
	}
	selected, err := normalized.scopeRegistry.Resolve(name)
	if err != nil {
		return ScopeContext{}, err
	}
	if err := normalized.ScopeAuthorizer.Authorize(ctx, selected.Ref, operation); err != nil {
		return ScopeContext{}, err
	}
	return selected, nil
}

// ScopeContext is the immutable public-name to durable-identity mapping used
// by host composition. Filesystem resolution enriches it later.
type ScopeContext struct {
	Name     domain.ScopeName
	Ref      domain.ScopeRef
	Disabled bool
	Sources  []domain.Source
}

// ScopeRegistry is a deterministic read-only set of configured scopes.
type ScopeRegistry struct {
	defaultName  domain.ScopeName
	legacyLayout bool
	ordered      []ScopeContext
	byName       map[domain.ScopeName]ScopeContext
}

// Default returns the configured enabled default scope.
func (registry *ScopeRegistry) Default() ScopeContext {
	if registry == nil {
		return ScopeContext{}
	}
	return cloneScopeContext(registry.byName[registry.defaultName])
}

// Resolve validates and resolves one public scope name. A blank name selects
// the configured default.
func (registry *ScopeRegistry) Resolve(name domain.ScopeName) (ScopeContext, error) {
	if registry == nil {
		return ScopeContext{}, fmt.Errorf("%w: registry is unavailable", ErrScopeConfigInvalid)
	}
	if strings.TrimSpace(string(name)) == "" {
		return registry.Default(), nil
	}
	validated, err := domain.ParseScopeName(string(name))
	if err != nil {
		return ScopeContext{}, err
	}
	context, ok := registry.byName[validated]
	if !ok {
		return ScopeContext{}, fmt.Errorf("%w: %q", domain.ErrScopeUnknown, validated)
	}
	if context.Disabled {
		return ScopeContext{}, fmt.Errorf("%w: %q", domain.ErrScopeDisabled, validated)
	}
	return cloneScopeContext(context), nil
}

// All returns configured scopes in stable public-name order.
func (registry *ScopeRegistry) All() []ScopeContext {
	if registry == nil {
		return nil
	}
	contexts := make([]ScopeContext, len(registry.ordered))
	for index := range registry.ordered {
		contexts[index] = cloneScopeContext(registry.ordered[index])
	}
	return contexts
}

// DefaultName returns the configured public default name.
func (registry *ScopeRegistry) DefaultName() domain.ScopeName {
	if registry == nil {
		return ""
	}
	return registry.defaultName
}

// ScopeRegistry validates this configuration and returns its immutable
// normalized registry without opening files or other runtime resources.
func (config Config) ScopeRegistry() (*ScopeRegistry, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return normalized.scopeRegistry, nil
}

func normalizeScopeConfiguration(config Config) (Config, error) {
	legacyLayout := len(config.Scopes) == 0
	if config.scopeRegistry != nil {
		legacyLayout = config.scopeRegistry.legacyLayout
	}
	defaultName := config.DefaultScopeName
	if strings.TrimSpace(string(defaultName)) == "" {
		defaultName = DefaultPublicScope
	}
	validatedDefault, err := domain.ParseScopeName(string(defaultName))
	if err != nil {
		return Config{}, fmt.Errorf("%w: default scope: %v", ErrScopeConfigInvalid, err)
	}

	var scopes []ScopeConfig
	if len(config.Scopes) == 0 {
		ref := domain.ScopeRef(strings.TrimSpace(string(config.Scope)))
		if ref == "" {
			ref = DefaultScope
		}
		sources, normalizeErr := normalizeSources(config.Workspace, "", config.Sources)
		if normalizeErr != nil {
			return Config{}, normalizeErr
		}
		scopes = []ScopeConfig{{Name: validatedDefault, Ref: ref, Sources: sources}}
	} else {
		if len(config.Sources) != 0 && config.scopeRegistry == nil {
			return Config{}, fmt.Errorf("%w: legacy sources and scopes cannot both be configured", ErrScopeConfigInvalid)
		}
		scopes = make([]ScopeConfig, len(config.Scopes))
		copy(scopes, config.Scopes)
	}

	seenNames := make(map[domain.ScopeName]struct{}, len(scopes))
	seenRefs := make(map[domain.ScopeRef]domain.ScopeName, len(scopes))
	normalizedScopes := make([]ScopeConfig, 0, len(scopes))
	for _, configured := range scopes {
		name, parseErr := domain.ParseScopeName(string(configured.Name))
		if parseErr != nil {
			return Config{}, fmt.Errorf("%w: %v", ErrScopeConfigInvalid, parseErr)
		}
		if _, exists := seenNames[name]; exists {
			return Config{}, fmt.Errorf("%w: duplicate scope name %q", ErrScopeConfigInvalid, name)
		}
		seenNames[name] = struct{}{}

		ref := domain.ScopeRef(strings.TrimSpace(string(configured.Ref)))
		if ref == "" {
			if name == validatedDefault {
				ref = domain.ScopeRef(strings.TrimSpace(string(config.Scope)))
				if ref == "" {
					ref = DefaultScope
				}
			} else {
				ref = domain.ScopeRef("scope:" + string(name))
			}
		}
		legacyRef := domain.ScopeRef(strings.TrimSpace(string(config.Scope)))
		if name == validatedDefault && configured.Ref != "" && legacyRef != "" && legacyRef != DefaultScope && ref != legacyRef {
			return Config{}, fmt.Errorf(
				"%w: legacy default reference %q conflicts with scope %q reference %q",
				ErrScopeConfigInvalid, legacyRef, name, ref,
			)
		}
		if previous, exists := seenRefs[ref]; exists {
			return Config{}, fmt.Errorf("%w: scopes %q and %q share durable reference %q", ErrScopeConfigInvalid, previous, name, ref)
		}
		seenRefs[ref] = name
		sources, normalizeErr := normalizeSources(config.Workspace, "", configured.Sources)
		if normalizeErr != nil {
			return Config{}, fmt.Errorf("%w: scope %q sources: %v", ErrScopeConfigInvalid, name, normalizeErr)
		}
		normalizedScopes = append(normalizedScopes, ScopeConfig{
			Name: name, Ref: ref, Disabled: configured.Disabled, Sources: sources,
		})
	}
	sort.Slice(normalizedScopes, func(left, right int) bool {
		return normalizedScopes[left].Name < normalizedScopes[right].Name
	})

	contexts := make([]ScopeContext, 0, len(normalizedScopes))
	var defaultContext ScopeContext
	defaultFound := false
	for _, scope := range normalizedScopes {
		context := ScopeContext{
			Name: scope.Name, Ref: scope.Ref, Disabled: scope.Disabled, Sources: cloneSources(scope.Sources),
		}
		contexts = append(contexts, context)
		if scope.Name == validatedDefault {
			defaultFound = true
			defaultContext = context
		}
	}
	if !defaultFound {
		return Config{}, fmt.Errorf("%w: default scope %q is not configured", ErrScopeConfigInvalid, validatedDefault)
	}
	if defaultContext.Disabled {
		return Config{}, fmt.Errorf("%w: default scope %q is disabled", ErrScopeConfigInvalid, validatedDefault)
	}
	registry := newScopeRegistry(validatedDefault, contexts, legacyLayout)
	config.DefaultScopeName = validatedDefault
	config.Scopes = normalizedScopes
	config.Scope = defaultContext.Ref
	config.Sources = cloneSources(defaultContext.Sources)
	config.scopeRegistry = registry
	return config, nil
}

func newScopeRegistry(defaultName domain.ScopeName, contexts []ScopeContext, legacyLayout bool) *ScopeRegistry {
	registry := &ScopeRegistry{
		defaultName:  defaultName,
		legacyLayout: legacyLayout,
		ordered:      make([]ScopeContext, len(contexts)),
		byName:       make(map[domain.ScopeName]ScopeContext, len(contexts)),
	}
	for index := range contexts {
		context := cloneScopeContext(contexts[index])
		registry.ordered[index] = context
		registry.byName[context.Name] = context
	}
	return registry
}

func cloneScopeContext(context ScopeContext) ScopeContext {
	context.Sources = cloneSources(context.Sources)
	return context
}
