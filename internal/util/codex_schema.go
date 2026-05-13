package util

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// NormalizeCodexToolSchema removes nullable array-valued schema keywords that
// Codex rejects while preserving valid schema structure.
func NormalizeCodexToolSchema(raw string) []byte {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || !ojson.Valid(raw) {
		return []byte(`{"type":"object","properties":{}}`)
	}

	schema := ojson.NewDocString(raw)
	for _, path := range codexSchemaNullArrayFieldPaths(raw) {
		_ = schema.Delete(path)
	}
	return schema.Bytes()
}

func codexSchemaNullArrayFieldPaths(raw string) []string {
	arrayFields := map[string]struct{}{
		"allOf":    {},
		"anyOf":    {},
		"enum":     {},
		"oneOf":    {},
		"required": {},
	}

	var paths []string
	walkCodexSchemaNullArrayFields(ojson.Parse(raw), "", arrayFields, &paths)
	return paths
}

func walkCodexSchemaNullArrayFields(value ojson.Result, path string, arrayFields map[string]struct{}, paths *[]string) {
	if value.IsArray() {
		items := value.Array()
		for i, item := range items {
			walkCodexSchemaNullArrayFields(item, joinCodexSchemaPath(path, strconv.Itoa(i)), arrayFields, paths)
		}
		return
	}
	if !value.IsObject() {
		return
	}

	value.ForEach(func(key, child ojson.Result) bool {
		keyString := key.String()
		childPath := joinCodexSchemaPath(path, ojson.Escape(keyString))
		if _, ok := arrayFields[keyString]; ok && child.Type == ojson.Null {
			*paths = append(*paths, childPath)
			return true
		}
		walkCodexSchemaNullArrayFields(child, childPath, arrayFields, paths)
		return true
	})
}

func joinCodexSchemaPath(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + "." + suffix
}

