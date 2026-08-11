package mcp

const (
	schemaTypeKey       = "type"
	schemaStringType    = "string"
	inlineSourceAdapter = "inline"
)

func objectSchema(required string) map[string]any {
	return map[string]any{
		schemaTypeKey: "object",
		"required":    []string{required},
		"properties": map[string]any{
			required: map[string]any{schemaTypeKey: schemaStringType},
		},
	}
}

func ingestSchema() map[string]any {
	return map[string]any{
		schemaTypeKey: "object",
		"properties": map[string]any{
			"content":         map[string]any{schemaTypeKey: schemaStringType},
			"uri":             map[string]any{schemaTypeKey: schemaStringType},
			"media_type":      map[string]any{schemaTypeKey: schemaStringType},
			"origin":          map[string]any{schemaTypeKey: schemaStringType},
			"idempotency_key": map[string]any{schemaTypeKey: schemaStringType},
		},
	}
}

func cloneMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
