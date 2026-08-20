package models

import "testing"

func TestBuildResponse(t *testing.T) {
	availableModels := []map[string]any{
		{"id": "claude-z", "display_name": "Zebra", "max_tokens": 64000},
		{"id": "antigravity-claude-opus-4-6-dd-5-elbaf-edualc", "display_name": "Alpha"},
		{"id": "claude-c", "display_name": "Alpha"},
		{"id": "claude-b", "display_name": "Beta"},
	}

	response := BuildResponse(availableModels)
	models, ok := response["data"].([]map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want []map[string]any", response["data"])
	}

	wantIDs := []string{"antigravity-claude-opus-4-6-dd-5-elbaf-edualc", "claude-c", "claude-b", "claude-z"}
	if len(models) != len(wantIDs) {
		t.Fatalf("len(data) = %d, want %d", len(models), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got, _ := models[i]["id"].(string); got != want {
			t.Fatalf("data[%d].id = %q, want %q", i, got, want)
		}
	}
	if got := models[3]["max_tokens"]; got != 64000 {
		t.Fatalf("max_tokens = %v, want 64000", got)
	}
	if got := response["has_more"]; got != false {
		t.Fatalf("has_more = %v, want false", got)
	}
	if got := response["first_id"]; got != wantIDs[0] {
		t.Fatalf("first_id = %v, want %q", got, wantIDs[0])
	}
	if got := response["last_id"]; got != wantIDs[len(wantIDs)-1] {
		t.Fatalf("last_id = %v, want %q", got, wantIDs[len(wantIDs)-1])
	}

	if got := availableModels[1]["id"]; got != "antigravity-claude-opus-4-6-dd-5-elbaf-edualc" {
		t.Fatalf("BuildResponse mutated input id to %v", got)
	}
	if got := availableModels[0]["id"]; got != "claude-z" {
		t.Fatalf("BuildResponse reordered input: first id = %v", got)
	}
}

func TestBuildResponseEmpty(t *testing.T) {
	response := BuildResponse(nil)
	models, ok := response["data"].([]map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want []map[string]any", response["data"])
	}
	if len(models) != 0 {
		t.Fatalf("len(data) = %d, want 0", len(models))
	}
	if response["first_id"] != "" || response["last_id"] != "" {
		t.Fatalf("empty response IDs = (%v, %v), want empty", response["first_id"], response["last_id"])
	}
}
