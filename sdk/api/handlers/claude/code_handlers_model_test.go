package claude

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestSortClaudeModelsByDisplayName(t *testing.T) {
	models := []map[string]any{
		{"id": "claude-b", "display_name": "Zebra"},
		{"id": "claude-a", "display_name": "Alpha"},
		{"id": "claude-c", "display_name": "Alpha"},
		{"id": "claude-d", "display_name": "Beta"},
	}
	sortClaudeModelsByDisplayName(models)
	wantIDs := []string{"claude-a", "claude-c", "claude-d", "claude-b"}
	for i, want := range wantIDs {
		got, _ := models[i]["id"].(string)
		if got != want {
			t.Fatalf("models[%d].id = %q, want %q", i, got, want)
		}
	}
}

func TestClaudeModelsResponseUsesConfiguredDisplayName(t *testing.T) {
	const clientID = "claude-display-name-catalog-test"
	const modelID = "claude-display-name-catalog-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: "test", DisplayName: "Configured Claude Name",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).ClaudeModels(ctx)
	var response struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model.ID == modelID {
			if model.DisplayName != "Configured Claude Name" {
				t.Fatalf("display_name = %q, want Configured Claude Name", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", modelID)
}

func TestClaudeModelsKeepsRawModelIDs(t *testing.T) {
	const clientID = "claude-raw-id-catalog-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{
		{ID: "antigravity-claude-sonnet-4-6", Object: "model", OwnedBy: "test", DisplayName: "Antigravity"},
		{ID: "gpt-4o", Object: "model", OwnedBy: "test", DisplayName: "GPT-4o"},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).ClaudeModels(ctx)
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	found := map[string]bool{}
	for _, model := range response.Data {
		found[model.ID] = true
		if model.ID == "claude-fable-5-dd-o4-tpg" || model.ID == "claude-fable-5-dd-6-4-tennos-edualc-ytivargitna" {
			t.Fatalf("model id was rewritten: %q", model.ID)
		}
	}
	if !found["antigravity-claude-sonnet-4-6"] {
		t.Fatalf("expected raw antigravity model id, got %#v", found)
	}
	if !found["gpt-4o"] {
		t.Fatalf("expected raw gpt-4o model id, got %#v", found)
	}
}
