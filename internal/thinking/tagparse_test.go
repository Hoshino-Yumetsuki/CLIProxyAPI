package thinking

import (
	"testing"
)

func TestFindThinkingStartTag(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect int
	}{
		{"simple", "<thinking>hello", 0},
		{"with prefix", "some text<thinking>hello", 9},
		{"not found", "no tag here", -1},
		{"backtick before", "`<thinking>`", -1},
		{"backtick after", "<thinking>`code", -1},
		{"double quote before", `"<thinking>"`, -1},
		{"single quote before", `'<thinking>'`, -1},
		{"backslash before", `\<thinking>`, -1},
		{"quoted then real", "`<thinking>` then <thinking>real", 18},
		{"multiple quoted", "```<thinking>``` and `<thinking>` and <thinking>ok", 38},
		{"empty", "", -1},
		{"partial tag", "<think", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindThinkingStartTag(tt.input)
			if got != tt.expect {
				t.Errorf("FindThinkingStartTag(%q) = %d, want %d", tt.input, got, tt.expect)
			}
		})
	}
}

func TestFindThinkingEndTag(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect int
	}{
		{"with double newline", "content</thinking>\n\nmore", 7},
		{"at start", "</thinking>\n\n", 0},
		{"not found", "no tag here", -1},
		{"no newlines after", "</thinking>text", -1},
		{"only one newline", "</thinking>\n", -1},
		{"backtick before", "`</thinking>`\n\n", -1},
		{"backtick after", "</thinking>`\n\n", -1},
		{"quoted then real", "`</thinking>` </thinking>\n\n", 14},
		{"insufficient bytes after", "</thinking>\n", -1},
		{"empty", "", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindThinkingEndTag(tt.input)
			if got != tt.expect {
				t.Errorf("FindThinkingEndTag(%q) = %d, want %d", tt.input, got, tt.expect)
			}
		})
	}
}

func TestFindThinkingEndTagAtEnd(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect int
	}{
		{"at end", "content</thinking>", 7},
		{"trailing whitespace", "content</thinking>  \n", 7},
		{"trailing text", "content</thinking>more", -1},
		{"backtick before", "content`</thinking>", -1},
		{"quoted then real at end", "`</thinking>` real</thinking>", 18},
		{"empty", "", -1},
		{"just tag", "</thinking>", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindThinkingEndTagAtEnd(tt.input)
			if got != tt.expect {
				t.Errorf("FindThinkingEndTagAtEnd(%q) = %d, want %d", tt.input, got, tt.expect)
			}
		})
	}
}

func TestExtractThinkingFromText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		segments []ThinkingTextSegment
	}{
		{
			name:  "no tags",
			input: "just regular text",
			segments: []ThinkingTextSegment{
				{Text: "just regular text", IsThinking: false},
			},
		},
		{
			name:  "thinking then text",
			input: "<thinking>\nI need to think\n</thinking>\n\nHere is my answer",
			segments: []ThinkingTextSegment{
				{Text: "I need to think\n", IsThinking: true},
				{Text: "Here is my answer", IsThinking: false},
			},
		},
		{
			name:  "only thinking",
			input: "<thinking>deep thoughts</thinking>",
			segments: []ThinkingTextSegment{
				{Text: "deep thoughts", IsThinking: true},
			},
		},
		{
			name:  "quoted tags not extracted",
			input: "Use `<thinking>` tags like this",
			segments: []ThinkingTextSegment{
				{Text: "Use `<thinking>` tags like this", IsThinking: false},
			},
		},
		{
			name:  "text before thinking",
			input: "prefix text<thinking>thoughts</thinking>\n\nsuffix text",
			segments: []ThinkingTextSegment{
				{Text: "prefix text", IsThinking: false},
				{Text: "thoughts", IsThinking: true},
				{Text: "suffix text", IsThinking: false},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractThinkingFromText(tt.input)
			if len(got) != len(tt.segments) {
				t.Fatalf("got %d segments, want %d.\ngot: %+v", len(got), len(tt.segments), got)
			}
			for i, seg := range got {
				if seg.Text != tt.segments[i].Text || seg.IsThinking != tt.segments[i].IsThinking {
					t.Errorf("segment[%d] = {%q, %v}, want {%q, %v}",
						i, seg.Text, seg.IsThinking,
						tt.segments[i].Text, tt.segments[i].IsThinking)
				}
			}
		})
	}
}

func TestIsThinkingEnabledInAntigravityRequest(t *testing.T) {
	tests := []struct {
		name   string
		json   string
		expect bool
	}{
		{"includeThoughts true", `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`, true},
		{"includeThoughts false", `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":false}}}}`, false},
		{"budget positive", `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}}`, true},
		{"budget auto", `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}}`, true},
		{"budget zero", `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}}`, false},
		{"level set", `{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"THINKING_LEVEL_HIGH"}}}}`, true},
		{"level none", `{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"THINKING_LEVEL_NONE"}}}}`, false},
		{"no config", `{"request":{"generationConfig":{}}}`, false},
		{"empty", `{}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsThinkingEnabledInAntigravityRequest([]byte(tt.json))
			if got != tt.expect {
				t.Errorf("IsThinkingEnabledInAntigravityRequest(%s) = %v, want %v", tt.json, got, tt.expect)
			}
		})
	}
}
