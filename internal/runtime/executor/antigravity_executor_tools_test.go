package executor

import (
	"context"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestAntigravityBuildRequestMixedTools(t *testing.T) {
	const function = `{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]}`
	const mixed = `[` + function + `,{"googleSearch":{}}]`
	cases := []struct {
		name   string
		tools  string
		config string
		path   string
		want   bool
	}{
		{name: "search", tools: mixed, want: true},
		{name: "code_execution", tools: `[{"codeExecution":{}},` + function + `]`, want: true},
		{name: "url_context_same_tool", tools: `[{"functionDeclarations":[{"name":"lookup"}],"urlContext":{}}]`, want: true},
		{name: "snake_case", tools: `[{"function_declarations":[{"name":"lookup"}]},{"google_search":{}}]`, config: `,"tool_config":{"function_calling_config":{"mode":"AUTO","allowed_function_names":["lookup"]}}`, path: "request.tool_config.include_server_side_tool_invocations", want: true},
		{name: "snake_code_execution", tools: `[` + function + `,{"code_execution":{}}]`, want: true},
		{name: "snake_url_context", tools: `[` + function + `,{"url_context":{}}]`, want: true},
		{name: "existing_config", tools: mixed, config: `,"toolConfig":{"functionCallingConfig":{"mode":"AUTO","allowedFunctionNames":["lookup"]},"retrievalConfig":{"latLng":{"latitude":1}}}`, want: true},
		{name: "disabled_flag", tools: mixed, config: `,"toolConfig":{"includeServerSideToolInvocations":false}`, want: true},
		{name: "snake_flag", tools: mixed, config: `,"toolConfig":{"include_server_side_tool_invocations":false}`, path: "request.toolConfig.include_server_side_tool_invocations", want: true},
		{name: "functions_only", tools: `[` + function + `]`},
		{name: "builtins_only", tools: `[{"googleSearch":{}},{"codeExecution":{}}]`},
		{name: "empty_functions", tools: `[{"functionDeclarations":[]},{"googleSearch":{}}]`},
		{name: "no_tools", tools: `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"tools":` + tc.tools + tc.config + `}}`)
			executor := NewAntigravityExecutor(nil)
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-test"}}
			request, errBuild := executor.buildRequest(context.Background(), auth, "token", "gemini-3.8-flash", payload, false, "", "https://example.invalid")
			if errBuild != nil {
				t.Fatal(errBuild)
			}
			wire, errRead := io.ReadAll(request.Body)
			if errClose := request.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if errRead != nil {
				t.Fatal(errRead)
			}
			path := tc.path
			if path == "" {
				path = "request.toolConfig.includeServerSideToolInvocations"
			}
			flag := gjson.GetBytes(wire, path)
			if tc.want && flag.Type != gjson.True || !tc.want && flag.Exists() {
				t.Fatalf("%s = %s, want enabled=%v: %s", path, flag.Raw, tc.want, wire)
			}
			for _, preserved := range []string{
				"request.toolConfig.functionCallingConfig.mode",
				"request.toolConfig.functionCallingConfig.allowedFunctionNames.0",
				"request.tool_config.function_calling_config.mode",
				"request.tool_config.function_calling_config.allowed_function_names.0",
				"request.toolConfig.retrievalConfig.latLng.latitude",
			} {
				if original := gjson.GetBytes(payload, preserved); original.Exists() && gjson.GetBytes(wire, preserved).Value() != original.Value() {
					t.Fatalf("changed %s: %s", preserved, wire)
				}
			}
		})
	}
}

func TestPayloadBarrierAntigravityMixedTools(t *testing.T) {
	for _, filter := range []bool{false, true} {
		name := "override"
		if filter {
			name = "filter"
		}
		t.Run(name, func(t *testing.T) {
			const path = "toolConfig.includeServerSideToolInvocations"
			models := []config.PayloadModelRule{{Name: "gemini-*"}}
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{path: false}}}}}
			if filter {
				cfg.Payload = config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: models, Params: []string{path, "contents.0"}}}}
			}
			payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"user","parts":[{"text":"second"}]}],"tools":[{"functionDeclarations":[{"name":"lookup"}]},{"googleSearch":{}}]}}`)
			req := cliproxyexecutor.Request{Model: "gemini-3.8-flash"}
			ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", req.Model, "antigravity", "request", payload, req, cliproxyexecutor.Options{}))
			executor := NewAntigravityExecutor(cfg)
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-test"}}
			for _, stream := range []bool{false, true} {
				request, errBuild := executor.buildRequest(ctx, auth, "token", req.Model, payload, stream, "", "https://example.invalid")
				if errBuild != nil {
					t.Fatal(errBuild)
				}
				wire, errRead := io.ReadAll(request.Body)
				if errClose := request.Body.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				if errRead != nil {
					t.Fatal(errRead)
				}
				flag := gjson.GetBytes(wire, "request."+path)
				if filter && flag.Exists() || !filter && flag.Type != gjson.False {
					t.Fatalf("built-in processing undid payload rule: %s", wire)
				}
				if filter && gjson.GetBytes(wire, "request.contents.#").Int() != 1 {
					t.Fatalf("filter did not run exactly once: %s", wire)
				}
			}
		})
	}
}
