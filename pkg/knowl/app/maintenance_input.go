package app

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// MaxMaintenanceRequestBytes is the supported local current-prompt ceiling.
const MaxMaintenanceRequestBytes = 4 << 20

var (
	// ErrMaintenanceInputLimit identifies input that cannot fit its request budget.
	ErrMaintenanceInputLimit = errors.New("maintenance input exceeds budget")
	// ErrMaintenanceInputInvalid identifies invalid limits or unencodable input.
	ErrMaintenanceInputInvalid = errors.New("invalid maintenance input")
)

// NormalizeMaintenanceInputLimits selects the finite default or validates a cap.
func NormalizeMaintenanceInputLimits(limits knowl.MaintenanceInputLimits) (knowl.MaintenanceInputLimits, error) {
	if limits == (knowl.MaintenanceInputLimits{}) {
		limits.MaxRequestBytes = MaxMaintenanceRequestBytes
	}
	if limits.MaxRequestBytes <= 0 || limits.MaxRequestBytes > MaxMaintenanceRequestBytes {
		return knowl.MaintenanceInputLimits{}, ErrMaintenanceInputInvalid
	}
	return limits, nil
}

type sourceWirePage struct {
	ID              knowl.PageID           `json:"id"`
	Path            string                 `json:"path"`
	Digest          string                 `json:"digest"`
	Title           string                 `json:"title"`
	Content         string                 `json:"content"`
	OKF             *okf.Metadata          `json:"okf,omitempty"`
	SourceRefs      []string               `json:"source_refs,omitempty"`
	SourceDocument  *knowl.SourceDocument  `json:"source_document,omitempty"`
	SourceDocuments []knowl.SourceDocument `json:"source_documents,omitempty"`
	Untrusted       bool                   `json:"untrusted"`
	UpdatedAt       time.Time              `json:"updated_at"`
}
type sourceWireInput struct {
	ContractVersion string                       `json:"contract_version"`
	InputLimits     knowl.MaintenanceInputLimits `json:"input_limits"`
	CatalogLimits   knowl.CatalogLimits          `json:"catalog_limits"`
	Scope           knowl.ScopeRef               `json:"scope"`
	Schema          knowl.SchemaDocument         `json:"schema"`
	Source          knowl.AcceptedSource         `json:"source"`
	SourceText      string                       `json:"source_text"`
	Pages           []sourceWirePage             `json:"pages"`
	Catalogs        []knowl.HierarchyCatalog     `json:"catalogs,omitempty"`
	Limits          knowl.ReadLimits             `json:"limits"`
}

// EncodeSourceMaintenanceRequest is the shared source envelope representation.
// It preserves full Content once, without changing application snapshots.
// The cap covers envelope bytes here; runtime sizing also counts its wrapper.
func EncodeSourceMaintenanceRequest(ctx context.Context, input knowl.MaintenanceInput) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limits, err := NormalizeMaintenanceInputLimits(input.InputLimits)
	if err != nil {
		return nil, err
	}
	// Bound header allocation before copying: required page keys alone exceed
	// 100 JSON bytes per page, even with empty values.
	if len(input.Pages) > limits.MaxRequestBytes/100 || len(input.Catalogs) > limits.MaxRequestBytes {
		return nil, ErrMaintenanceInputLimit
	}
	identityBytes := len(input.Source.Source.Adapter)
	for _, part := range []string{input.Source.Source.ID, input.Source.Version.Version} {
		if identityBytes > limits.MaxRequestBytes-len(part) {
			return nil, ErrMaintenanceInputLimit
		}
		identityBytes += len(part)
	}
	if identityBytes > limits.MaxRequestBytes-2 {
		return nil, ErrMaintenanceInputLimit
	}
	pages := make([]sourceWirePage, len(input.Pages))
	for i, p := range input.Pages {
		pages[i] = sourceWirePage{p.ID, p.Path, p.Digest, p.Title, p.Content, p.OKF, p.SourceRefs, p.SourceDocument, p.SourceDocuments, p.Untrusted, p.UpdatedAt}
	}
	envelope := struct {
		Operation string          `json:"operation"`
		Input     sourceWireInput `json:"input"`
		Schema    string          `json:"required_schema_digest"`
		SourceRef string          `json:"required_source_ref"`
	}{"source_maintenance", sourceWireInput{input.ContractVersion, limits, input.CatalogLimits, input.Scope, input.Schema, input.Source, input.SourceText, pages, input.Catalogs, input.Limits}, input.Schema.Digest, SourceRefKey(input.Source)}
	preflight := requestPreflight{ctx: ctx, bytes: limits.MaxRequestBytes, nodes: limits.MaxRequestBytes}
	if err := preflight.visit(reflect.ValueOf(envelope), 0, false); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrMaintenanceInputInvalid
	}
	if len(encoded) > limits.MaxRequestBytes {
		return nil, ErrMaintenanceInputLimit
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	return encoded, nil
}

// requestPreflight bounds raw strings, collection work and recursive metadata
// before standard JSON allocation. JSON expansion is finite (at most six bytes
// per input byte); this lower-bound pass does not replace exact final accounting.
type requestPreflight struct {
	ctx          context.Context
	bytes, nodes int
}

var requestTimeType = reflect.TypeFor[time.Time]()
var requestMarshalerType = reflect.TypeFor[json.Marshaler]()
var requestTextMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()

func (p *requestPreflight) text(s string) error {
	if len(s) > p.bytes {
		return ErrMaintenanceInputLimit
	}
	if !utf8.ValidString(s) {
		return ErrMaintenanceInputInvalid
	}
	p.bytes -= len(s)
	return nil
}
func (p *requestPreflight) visit(v reflect.Value, depth int, transport bool) error {
	if err := contextErr(p.ctx); err != nil {
		return err
	}
	if depth > 128 {
		return ErrMaintenanceInputInvalid
	}
	if p.nodes == 0 {
		return ErrMaintenanceInputLimit
	}
	p.nodes--
	if !v.IsValid() {
		return nil
	}
	if transport {
		switch v.Kind() {
		case reflect.Pointer, reflect.Struct, reflect.Array:
			return ErrMaintenanceInputInvalid
		}
	}
	if v.Type() == requestTimeType || v.Type() == reflect.PointerTo(requestTimeType) {
		return nil
	}
	// Only the checked-in DTO's time values may supply custom JSON methods.
	if requestCustomMarshaler(v.Type()) || v.CanAddr() && requestCustomMarshaler(v.Addr().Type()) {
		return ErrMaintenanceInputInvalid
	}
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			return p.visit(v.Elem(), depth+1, true)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			return p.visit(v.Elem(), depth+1, transport)
		}
	case reflect.String:
		return p.text(v.String())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				if err := p.visit(v.Field(i), depth+1, transport); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem() == reflect.TypeFor[byte]() {
			if v.Len() > p.bytes {
				return ErrMaintenanceInputLimit
			}
			p.bytes -= v.Len()
			return nil
		}
		if v.Len() > p.nodes {
			return ErrMaintenanceInputLimit
		}
		for i := 0; i < v.Len(); i++ {
			if err := p.visit(v.Index(i), depth+1, transport); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return ErrMaintenanceInputInvalid
		}
		if v.Len() > p.nodes/2 {
			return ErrMaintenanceInputLimit
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := p.visit(iter.Key(), depth+1, transport); err != nil {
				return err
			}
			if err := p.visit(iter.Value(), depth+1, transport); err != nil {
				return err
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
	default:
		return ErrMaintenanceInputInvalid
	}
	return nil
}

func requestCustomMarshaler(t reflect.Type) bool {
	return t.Implements(requestMarshalerType) || t.Implements(requestTextMarshalerType)
}
