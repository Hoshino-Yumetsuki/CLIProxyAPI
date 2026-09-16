package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// The xai-org/grok-build client builds the Responses payload with the system
// prompt in the top-level `instructions` field. A `developer`-role input message
// is not honoured as a system prompt, and an empty `instructions` string tells
// the server the request has no system prompt at all, so the caller's agent
// directives have to be hoisted.
func TestNormalizeXAIInstructions(t *testing.T) {
	tests := []struct {
		name              string
		body              string
		wantInstructions  string
		wantInstrPresent  bool
		wantInputLen      int
		wantInputRoleTail string
	}{
		{
			name: "system message hoisted out of input",
			body: `{"model":"grok-4.6","instructions":"","input":[` +
				`{"type":"message","role":"system","content":[{"type":"input_text","text":"SYS DIRECTIVE"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]}]}`,
			wantInstructions:  "SYS DIRECTIVE",
			wantInstrPresent:  true,
			wantInputLen:      1,
			wantInputRoleTail: "user",
		},
		{
			name: "developer message hoisted out of input",
			body: `{"model":"grok-4.6","instructions":"","input":[` +
				`{"type":"message","role":"developer","content":[{"type":"input_text","text":"DEV DIRECTIVE"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]}]}`,
			wantInstructions:  "DEV DIRECTIVE",
			wantInstrPresent:  true,
			wantInputLen:      1,
			wantInputRoleTail: "user",
		},
		{
			name: "multiple directives joined in order",
			body: `{"model":"grok-4.6","instructions":"","input":[` +
				`{"type":"message","role":"system","content":[{"type":"input_text","text":"FIRST"}]},` +
				`{"type":"message","role":"developer","content":[{"type":"input_text","text":"SECOND"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}]}`,
			wantInstructions:  "FIRST\n\nSECOND",
			wantInstrPresent:  true,
			wantInputLen:      1,
			wantInputRoleTail: "user",
		},
		{
			name: "existing instructions preserved ahead of hoisted text",
			body: `{"model":"grok-4.6","instructions":"EXISTING","input":[` +
				`{"type":"message","role":"system","content":[{"type":"input_text","text":"LATE"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}]}`,
			wantInstructions:  "EXISTING\n\nLATE",
			wantInstrPresent:  true,
			wantInputLen:      1,
			wantInputRoleTail: "user",
		},
		{
			name: "plain string content is hoisted",
			body: `{"model":"grok-4.6","input":[` +
				`{"type":"message","role":"system","content":"PLAIN SYS"},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}]}`,
			wantInstructions:  "PLAIN SYS",
			wantInstrPresent:  true,
			wantInputLen:      1,
			wantInputRoleTail: "user",
		},
		{
			name: "empty instructions dropped when nothing to hoist",
			body: `{"model":"grok-4.6","instructions":"","input":[` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}]}`,
			wantInstrPresent:  false,
			wantInputLen:      1,
			wantInputRoleTail: "user",
		},
		{
			name: "non-empty instructions kept when nothing to hoist",
			body: `{"model":"grok-4.6","instructions":"KEEP","input":[` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}]}`,
			wantInstructions:  "KEEP",
			wantInstrPresent:  true,
			wantInputLen:      1,
			wantInputRoleTail: "user",
		},
		{
			name: "non-message items preserved around a hoisted directive",
			body: `{"model":"grok-4.6","instructions":"","input":[` +
				`{"type":"message","role":"system","content":[{"type":"input_text","text":"SYS"}]},` +
				`{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
			wantInstructions:  "SYS",
			wantInstrPresent:  true,
			wantInputLen:      2,
			wantInputRoleTail: "function_call_output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeXAIInstructions([]byte(tt.body))

			instructions := gjson.GetBytes(got, "instructions")
			if tt.wantInstrPresent {
				if !instructions.Exists() {
					t.Fatalf("instructions absent; body=%s", got)
				}
				if instructions.String() != tt.wantInstructions {
					t.Fatalf("instructions = %q, want %q", instructions.String(), tt.wantInstructions)
				}
			} else if instructions.Exists() {
				t.Fatalf("instructions should be absent, got %q", instructions.String())
			}

			input := gjson.GetBytes(got, "input")
			if gotLen, wantLen := len(input.Array()), tt.wantInputLen; gotLen != wantLen {
				t.Fatalf("input length = %d, want %d; body=%s", gotLen, wantLen, got)
			}
			for _, item := range input.Array() {
				switch item.Get("role").String() {
				case "system", "developer":
					t.Fatalf("directive left in input; body=%s", got)
				}
			}
			items := input.Array()
			tail := items[len(items)-1]
			tailID := tail.Get("type").String()
			if tailID == "message" {
				tailID = tail.Get("role").String()
			}
			if tailID != tt.wantInputRoleTail {
				t.Fatalf("last input item = %q, want %q", tailID, tt.wantInputRoleTail)
			}
		})
	}
}

func TestNormalizeXAIInstructionsLeavesMalformedBodyAlone(t *testing.T) {
	for _, body := range []string{`{"model":"grok-4.6","input":"not-an-array"}`, `not json`} {
		if got := string(normalizeXAIInstructions([]byte(body))); got != body {
			t.Fatalf("body changed: got %q, want %q", got, body)
		}
	}
}

// End-to-end on the real preparation path: a chat-completions request whose
// system message is translated into a developer input item must reach upstream
// with its directives in `instructions`.
func TestXAIExecutorHoistsSystemPromptIntoInstructions(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			return
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"r","object":"response","created_at":0,"status":"completed","model":"grok-4.6","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-instructions",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	payload := []byte(`{"model":"grok-4.6","messages":[` +
		`{"role":"system","content":"AGENT RULES"},` +
		`{"role":"user","content":"do the task"}]}`)

	if _, err := executor.Execute(context.Background(), auth,
		cliproxyexecutor.Request{Model: "grok-4.6", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(bodies) == 0 {
		t.Fatal("no upstream request captured")
	}

	body := bodies[0]
	if got := gjson.GetBytes(body, "instructions").String(); got != "AGENT RULES" {
		t.Fatalf("instructions = %q, want %q; body=%s", got, "AGENT RULES", body)
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		switch item.Get("role").String() {
		case "system", "developer":
			t.Fatalf("system directive left in input; body=%s", body)
		}
	}
	if !strings.Contains(string(body), "do the task") {
		t.Fatalf("user turn lost; body=%s", body)
	}
}
