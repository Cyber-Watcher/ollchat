package ollama

import (
	"encoding/json"
	"strings"
	"testing"
)

// Схема стороннего сервера MCP уходит модели слово в слово (этап 121, Б3):
// items, вложенные объекты, anyOf и type списком в ToolProp не ложатся,
// и до Raw они бы молча пропали из запроса.
func TestRawParamsMarshalAsIs(t *testing.T) {
	raw := `{"type": "object",
		"properties": {
			"ids":   {"type": "array", "items": {"type": "integer"}, "description": "номера"},
			"where": {"anyOf": [{"type": "string"}, {"type": "object", "properties": {"x": {"type": "number"}}}]},
			"note":  {"type": ["string", "null"]}
		},
		"required": ["ids"]}`
	p, err := RawParams(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	tool := Tool{Type: "function", Function: ToolSpec{Name: "mcp__srv__x", Parameters: p}}
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"items":{"type":"integer"}`, `"anyOf":[`, `"type":["string","null"]`, `"required":["ids"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("в запросе нет %s: %s", want, b)
		}
	}
	// Разбор для подсказки о параметрах — терпимый.
	if p.Properties["ids"].Type != "array" || p.Properties["ids"].Description != "номера" {
		t.Errorf("ids: %+v", p.Properties["ids"])
	}
	if p.Properties["note"].Type != "string|null" || p.Properties["note"].Description != "" {
		t.Errorf("note: %+v", p.Properties["note"])
	}
	if len(p.Required) != 1 || p.Required[0] != "ids" {
		t.Errorf("required: %v", p.Required)
	}
}

// Без Raw — прежний вид: встроенные инструменты не меняются ни на байт.
func TestToolParamsWithoutRawUnchanged(t *testing.T) {
	p := ToolParams{Type: "object", Properties: map[string]ToolProp{
		"q": {Type: "string", Description: "вопрос"}}, Required: []string{"q"}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"object","properties":{"q":{"type":"string","description":"вопрос"}},"required":["q"]}`
	if string(b) != want {
		t.Errorf("получено %s\nждали    %s", b, want)
	}
}

// Схема, которую Ollama не разберёт в свои типы (api/types.go:
// ToolFunctionParameters, ToolProperty), отвергается здесь — иначе отказ
// получал бы весь /api/chat, а не один инструмент. Прежде описание числом
// принималось («нестроковое пропускается») и уходило в запрос.
func TestRawParamsRejectsWhatOllamaRejects(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{"q":{"type":"string","required":true}}}`,
		`{"type":"object","properties":{"q":{"type":"string","description":{"text":"x"}}}}`,
		`{"type":"object","properties":{"q":{"type":"string","description":5}}}`,
		`{"type":"object","properties":{"q":{"type":5}}}`,
		`{"type":"object","properties":{"q":{"type":"string","enum":"a"}}}`,
		`{"type":"object","properties":{"q":true}}`,
		`{"type":"object","properties":[]}`,
		`{"type":"object","required":"q"}`,
		`{"type":["object"]}`,
		`{"type":"object","properties":{"o":{"type":"object","properties":{"x":{"description":[1]}}}}}`,
		`{"type":"object","properties":{"o":{"anyOf":[{"type":{"a":1}}]}}}`,
	} {
		if _, err := RawParams(json.RawMessage(raw)); err == nil {
			t.Errorf("принята схема, которую Ollama отвергнет: %s", raw)
		}
	}
	// То, что Ollama берёт: свойство по имени description, items и $defs любые
	// (у неё это any), null вместо полей, enum разных типов.
	for _, raw := range []string{
		`{"type":"object","properties":{"description":{"type":"string","description":"текст"}}}`,
		`{"type":"object","properties":{"ids":{"type":"array","items":true}},"$defs":{"x":5}}`,
		`{"type":"object","properties":null,"required":null}`,
		`{"type":"object","properties":{"q":{"type":null,"enum":[1,"a",null]}}}`,
	} {
		if _, err := RawParams(json.RawMessage(raw)); err != nil {
			t.Errorf("отвергнута годная схема %s: %v", raw, err)
		}
	}
}

func TestRawParamsRejectsNonObject(t *testing.T) {
	for _, raw := range []string{`[]`, `"x"`, `null`, `{`, `5`} {
		if _, err := RawParams(json.RawMessage(raw)); err == nil {
			t.Errorf("%s принят как схема", raw)
		}
	}
}
