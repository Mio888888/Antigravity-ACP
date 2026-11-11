package adapter

import (
	"encoding/json"
	"strings"

	"antigravity-gateway/internal/acp"
)

func ParseDataURI(dataURI string) (mimeType string, base64Data string, ok bool) {
	if !strings.HasPrefix(dataURI, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(dataURI, "data:")
	semiIdx := strings.Index(rest, ";base64,")
	if semiIdx < 0 {
		return "", "", false
	}
	return rest[:semiIdx], rest[semiIdx+len(";base64,"):], true
}

func sanitizeSchemaNode(node map[string]any) map[string]any {
	if node == nil {
		return nil
	}

	// Normalize JSON Schema union types like `"type": ["string", "null"]` into
	// `"type": "string", "nullable": true` so Gemini/ACP function declarations
	// never reject or misformat union type arrays.
	switch rawType := node["type"].(type) {
	case []any:
		var primary string
		nullable := false
		for _, elem := range rawType {
			s, ok := elem.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "null" {
				nullable = true
				continue
			}
			if primary == "" && s != "" {
				primary = s
			}
		}
		if primary == "" {
			primary = "string"
		}
		node["type"] = primary
		if nullable {
			node["nullable"] = true
		}
	case []string:
		var primary string
		nullable := false
		for _, s := range rawType {
			s = strings.TrimSpace(s)
			if s == "null" {
				nullable = true
				continue
			}
			if primary == "" && s != "" {
				primary = s
			}
		}
		if primary == "" {
			primary = "string"
		}
		node["type"] = primary
		if nullable {
			node["nullable"] = true
		}
	}

	// Gemini rejects function declarations whose enum contains an empty string
	// ("enum[N]: cannot be empty"), failing the whole turn with INVALID_ARGUMENT. Clients
	// commonly use "" (or null) to mean "unset", so drop those values; omission already
	// expresses "unset", and null is preserved as nullable.
	if rawEnum, ok := node["enum"].([]any); ok {
		kept := make([]any, 0, len(rawEnum))
		for _, v := range rawEnum {
			switch ev := v.(type) {
			case nil:
				node["nullable"] = true
				continue
			case string:
				if strings.TrimSpace(ev) == "" {
					continue
				}
			}
			kept = append(kept, v)
		}
		if len(kept) == 0 {
			delete(node, "enum")
		} else {
			node["enum"] = kept
		}
	}

	if props, ok := node["properties"].(map[string]any); ok {
		for k, v := range props {
			if propMap, ok := v.(map[string]any); ok {
				props[k] = sanitizeSchemaNode(propMap)
			}
		}
	}

	if items, ok := node["items"].(map[string]any); ok {
		node["items"] = sanitizeSchemaNode(items)
	}

	for _, combKey := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := node[combKey].([]any); ok {
			for i, elem := range arr {
				if m, ok := elem.(map[string]any); ok {
					arr[i] = sanitizeSchemaNode(m)
				}
			}
		}
	}

	return node
}

func EnsureObjectSchema(raw json.RawMessage) map[string]any {
	var schema map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &schema)
	}
	if schema == nil {
		schema = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	schema = sanitizeSchemaNode(schema)
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	if schema["type"] == "object" {
		if _, ok := schema["properties"]; !ok {
			schema["properties"] = map[string]any{}
		}
	}
	return schema
}

func ToMCPToolDefinition(name, description string, rawSchema json.RawMessage) acp.ToolDefinition {
	return acp.ToolDefinition{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		InputSchema: EnsureObjectSchema(rawSchema),
	}
}
