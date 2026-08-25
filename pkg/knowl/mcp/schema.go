package mcp

const (
	schemaTypeKey       = "type"
	schemaStringType    = "string"
	schemaObjectType    = "object"
	schemaPropertiesKey = "properties"
	inlineSourceAdapter = "inline"
)

func objectSchema(required string) map[string]any {
	return map[string]any{
		schemaTypeKey: schemaObjectType,
		"required":    []string{required},
		schemaPropertiesKey: map[string]any{
			required: map[string]any{schemaTypeKey: schemaStringType},
		},
	}
}

func retrieveSchema() map[string]any {
	return map[string]any{
		schemaTypeKey: schemaObjectType,
		"required":    []string{"query"},
		schemaPropertiesKey: map[string]any{
			"query": map[string]any{schemaTypeKey: schemaStringType},
			"sources": map[string]any{
				schemaTypeKey: "array",
				"items":       map[string]any{schemaTypeKey: schemaStringType},
			},
		},
	}
}

func ingestSchema() map[string]any {
	return map[string]any{
		schemaTypeKey: schemaObjectType,
		schemaPropertiesKey: map[string]any{
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
