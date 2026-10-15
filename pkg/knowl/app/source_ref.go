package app

import (
	"mime"
	"net/url"
	"strings"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const canonicalSourceRefPrefix = "v2:"

const (
	// MaxLegacySourceRefCandidates bounds compatibility decoding of persisted
	// delimiter-based refs. New delimiter-bearing identities use canonical v2.
	MaxLegacySourceRefCandidates = 16
	// MaxAcceptedSourceResolveRefs bounds one page-scoped exact-ref lookup.
	MaxAcceptedSourceResolveRefs = 128
	// MaxAcceptedSourceResolveProbes bounds all candidate manifest paths in one lookup.
	MaxAcceptedSourceResolveProbes = 256
	// MaxAcceptedSourceResolveBytes bounds all requested manifests in one lookup.
	MaxAcceptedSourceResolveBytes = 4 << 20
)

// SourceRefIdentity is the reversible identity encoded by a citation ref.
type SourceRefIdentity struct {
	Source   knowl.SourceRef
	Revision string
}

// SourceRefNotFoundError identifies the exact missing accepted citation without
// exposing adapter/storage details. It preserves ErrSourceNotFound identity.
type SourceRefNotFoundError struct{ Ref string }

func (err *SourceRefNotFoundError) Error() string { return ErrSourceNotFound.Error() }
func (err *SourceRefNotFoundError) Unwrap() error { return ErrSourceNotFound }

// EncodeSourceRef returns the stable citation spelling for one exact source.
// Existing unambiguous refs retain their legacy adapter:id@revision spelling;
// delimiter-bearing identities use the collision-free v2 percent encoding.
func EncodeSourceRef(identity SourceRefIdentity) (string, error) {
	if err := ValidateSourceRefIdentity(identity); err != nil {
		return "", err
	}
	if sourceRefComponentsHaveCanonicalEdges(identity) &&
		!strings.Contains(identity.Source.Adapter, ":") &&
		!strings.Contains(identity.Source.ID, "@") &&
		!strings.Contains(identity.Revision, "@") {
		legacy := legacySourceRef(identity)
		if validEncodedSourceRef(legacy) {
			return legacy, nil
		}
	}
	encoded := canonicalSourceRefPrefix + percentEncodeSourceRefPart(identity.Source.Adapter) + ":" +
		percentEncodeSourceRefPart(identity.Source.ID) + ":" + percentEncodeSourceRefPart(identity.Revision)
	if len(encoded) > maxOperatorSourceRefBytes {
		return "", ErrSourceInvalid
	}
	return encoded, nil
}

// ParseSourceRefCandidates decodes one canonical ref or enumerates identities
// represented by a legacy ref whose opaque parts contain @. Legacy spellings
// with more than MaxLegacySourceRefCandidates delimiter splits fail closed;
// canonical v2 is the compatibility path for identities beyond that boundary.
func ParseSourceRefCandidates(ref string) ([]SourceRefIdentity, error) {
	if !validEncodedSourceRef(ref) {
		return nil, ErrSourceInvalid
	}
	if strings.HasPrefix(ref, canonicalSourceRefPrefix) && !strings.Contains(ref, "@") {
		parts := strings.Split(ref, ":")
		if len(parts) != 4 || parts[0] != "v2" {
			return nil, ErrSourceInvalid
		}
		adapter, err := percentDecodeSourceRefPart(parts[1])
		if err != nil {
			return nil, ErrSourceInvalid
		}
		id, err := percentDecodeSourceRefPart(parts[2])
		if err != nil {
			return nil, ErrSourceInvalid
		}
		revision, err := percentDecodeSourceRefPart(parts[3])
		if err != nil {
			return nil, ErrSourceInvalid
		}
		identity := SourceRefIdentity{Source: knowl.SourceRef{Adapter: adapter, ID: id}, Revision: revision}
		encoded, encodeErr := EncodeSourceRef(identity)
		if encodeErr != nil || encoded != ref {
			return nil, ErrSourceInvalid
		}
		return []SourceRefIdentity{identity}, nil
	}

	adapter, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return nil, ErrSourceInvalid
	}
	candidateCount := strings.Count(rest, "@")
	if candidateCount > MaxLegacySourceRefCandidates {
		return nil, ErrSourceRefLimit
	}
	candidates := make([]SourceRefIdentity, 0, candidateCount)
	for offset := 0; offset < len(rest); {
		relative := strings.IndexByte(rest[offset:], '@')
		if relative < 0 {
			break
		}
		separator := offset + relative
		identity := SourceRefIdentity{
			Source:   knowl.SourceRef{Adapter: adapter, ID: rest[:separator]},
			Revision: rest[separator+1:],
		}
		if ValidateSourceRefIdentity(identity) == nil && sourceRefComponentsHaveCanonicalEdges(identity) && legacySourceRef(identity) == ref {
			candidates = append(candidates, identity)
		}
		offset = separator + 1
	}
	if len(candidates) == 0 {
		return nil, ErrSourceInvalid
	}
	return candidates, nil
}

// ValidateSourceRefIdentity validates the bounded opaque parts accepted by the
// citation codec and workspace address derivation.
func ValidateSourceRefIdentity(identity SourceRefIdentity) error {
	for _, value := range []string{identity.Source.Adapter, identity.Source.ID, identity.Revision} {
		if !validOpaque(value, maxOperatorSourceRefBytes, false) {
			return ErrSourceInvalid
		}
	}
	return nil
}

// SourceRefKey is the stable citation key required in a plan.
func SourceRefKey(source knowl.AcceptedSource) string {
	encoded, _ := EncodeSourceRef(SourceRefIdentity{Source: source.Source, Revision: source.Version.Version})
	return encoded
}

// SourceRefMatches reports whether a canonical or legacy citation denotes the
// exact accepted identity. A colliding legacy spelling is intentionally not
// disambiguated here; trusted resolution must establish uniqueness first.
func SourceRefMatches(ref string, source knowl.AcceptedSource) bool {
	candidates, err := ParseSourceRefCandidates(ref)
	if err != nil {
		return false
	}
	want := SourceRefIdentity{Source: source.Source, Revision: source.Version.Version}
	for _, candidate := range candidates {
		if candidate == want {
			return true
		}
	}
	return false
}

// SourceRefAliases returns the canonical key and, when it differs, the legacy
// surface spelling retained for reading already-persisted pages.
func SourceRefAliases(source knowl.AcceptedSource) []string {
	canonical := SourceRefKey(source)
	legacy := legacySourceRef(SourceRefIdentity{Source: source.Source, Revision: source.Version.Version})
	if canonical == "" {
		if SourceRefMatches(legacy, source) {
			return []string{legacy}
		}
		return nil
	}
	if legacy == canonical || !SourceRefMatches(legacy, source) {
		return []string{canonical}
	}
	return []string{canonical, legacy}
}

// ValidateAcceptedSourceDescriptor validates complete configured-source
// provenance before immutable bytes are exposed to maintenance providers.
func ValidateAcceptedSourceDescriptor(source knowl.AcceptedSource) error {
	identity := SourceRefIdentity{Source: source.Source, Revision: source.Version.Version}
	mediaType, _, mediaErr := mime.ParseMediaType(source.MediaType)
	wantID := string(source.SourceDocument.SourceID) + "/" + string(source.SourceDocument.DocumentID)
	if strings.TrimSpace(string(source.Scope)) == "" || ValidateSourceRefIdentity(identity) != nil ||
		len(SourceRefAliases(source)) == 0 || source.SourceDocument == (knowl.SourceDocument{}) ||
		ValidateSourceDocument(source.SourceDocument) != nil || source.SourceDocument.Revision != source.Version.Version ||
		source.Source.ID != wantID || !validSHA256(source.Version.Digest) || source.Version.Digest != strings.ToLower(source.Version.Digest) ||
		mediaErr != nil || strings.TrimSpace(mediaType) == "" || !validManifestRef(source.ManifestRef) {
		return ErrSourceInvalid
	}
	return nil
}

func legacySourceRef(identity SourceRefIdentity) string {
	return identity.Source.Adapter + ":" + identity.Source.ID + "@" + identity.Revision
}

func sourceRefComponentsHaveCanonicalEdges(identity SourceRefIdentity) bool {
	return strings.TrimSpace(identity.Source.Adapter) == identity.Source.Adapter &&
		strings.TrimSpace(identity.Source.ID) == identity.Source.ID &&
		strings.TrimSpace(identity.Revision) == identity.Revision
}

func validEncodedSourceRef(ref string) bool {
	if ref == "" || len(ref) > maxOperatorSourceRefBytes || !utf8.ValidString(ref) || strings.TrimSpace(ref) != ref {
		return false
	}
	for _, character := range ref {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func percentEncodeSourceRefPart(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var encoded strings.Builder
	encoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-._~", rune(character)) {
			encoded.WriteByte(character)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexadecimal[character>>4])
		encoded.WriteByte(hexadecimal[character&0x0f])
	}
	return encoded.String()
}

func percentDecodeSourceRefPart(value string) (string, error) {
	decoded, err := url.PathUnescape(value)
	if err != nil || !utf8.ValidString(decoded) || percentEncodeSourceRefPart(decoded) != value {
		return "", ErrSourceInvalid
	}
	return decoded, nil
}
