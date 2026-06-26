package thinking

import (
	"strings"

	"github.com/tidwall/gjson"
)

const (
	thinkingStartTag = "<thinking>"
	thinkingEndTag   = "</thinking>"
	// thinkingEndTagWithNewlines is the end tag followed by the double newline
	// that separates thinking from the subsequent text content.
	thinkingEndTagWithNewlines = "</thinking>\n\n"
)

// ThinkingStartTagLen is the byte length of "<thinking>".
var ThinkingStartTagLen = len(thinkingStartTag)

// ThinkingEndTagWithNewlinesLen is the byte length of "</thinking>\n\n".
var ThinkingEndTagWithNewlinesLen = len(thinkingEndTagWithNewlines)

// ThinkingEndTagLen is the byte length of "</thinking>".
var ThinkingEndTagLen = len(thinkingEndTag)

func isQuoteChar(b byte) bool {
	return b == '`' || b == '"' || b == '\'' || b == '\\'
}

// FindThinkingStartTag finds the first unquoted <thinking> tag in buffer.
// Returns -1 if not found.
func FindThinkingStartTag(buffer string) int {
	searchStart := 0
	for {
		pos := strings.Index(buffer[searchStart:], thinkingStartTag)
		if pos < 0 {
			return -1
		}
		absPos := searchStart + pos

		hasQuoteBefore := absPos > 0 && isQuoteChar(buffer[absPos-1])
		afterPos := absPos + len(thinkingStartTag)
		hasQuoteAfter := afterPos < len(buffer) && isQuoteChar(buffer[afterPos])

		if !hasQuoteBefore && !hasQuoteAfter {
			return absPos
		}
		searchStart = absPos + 1
	}
}

// FindThinkingEndTag finds the first unquoted </thinking> tag followed by \n\n.
// Returns -1 if not found or if there aren't enough bytes after the tag to confirm \n\n.
func FindThinkingEndTag(buffer string) int {
	searchStart := 0
	for {
		pos := strings.Index(buffer[searchStart:], thinkingEndTag)
		if pos < 0 {
			return -1
		}
		absPos := searchStart + pos

		hasQuoteBefore := absPos > 0 && isQuoteChar(buffer[absPos-1])
		afterPos := absPos + len(thinkingEndTag)
		hasQuoteAfter := afterPos < len(buffer) && isQuoteChar(buffer[afterPos])

		if hasQuoteBefore || hasQuoteAfter {
			searchStart = absPos + 1
			continue
		}

		afterContent := buffer[afterPos:]
		if len(afterContent) < 2 {
			return -1
		}

		if strings.HasPrefix(afterContent, "\n\n") {
			return absPos
		}

		searchStart = absPos + 1
	}
}

// FindThinkingEndTagAtEnd finds an unquoted </thinking> tag at the buffer end
// (where everything after the tag is whitespace). Used for boundary events like
// stream end or tool_use arrival where \n\n may not follow.
// Returns -1 if not found.
func FindThinkingEndTagAtEnd(buffer string) int {
	searchStart := 0
	for {
		pos := strings.Index(buffer[searchStart:], thinkingEndTag)
		if pos < 0 {
			return -1
		}
		absPos := searchStart + pos

		hasQuoteBefore := absPos > 0 && isQuoteChar(buffer[absPos-1])
		afterPos := absPos + len(thinkingEndTag)
		hasQuoteAfter := afterPos < len(buffer) && isQuoteChar(buffer[afterPos])

		if hasQuoteBefore || hasQuoteAfter {
			searchStart = absPos + 1
			continue
		}

		if strings.TrimSpace(buffer[afterPos:]) == "" {
			return absPos
		}

		searchStart = absPos + 1
	}
}

// ExtractThinkingFromText parses text for <thinking>...</thinking> tags and
// splits it into thinking and text segments. Used for non-streaming responses.
// Returns a slice of ThinkingTextSegment in order of appearance.
func ExtractThinkingFromText(text string) []ThinkingTextSegment {
	var segments []ThinkingTextSegment
	remaining := text

	for {
		startPos := FindThinkingStartTag(remaining)
		if startPos < 0 {
			if remaining != "" {
				segments = append(segments, ThinkingTextSegment{Text: remaining, IsThinking: false})
			}
			break
		}

		if startPos > 0 {
			before := remaining[:startPos]
			if strings.TrimSpace(before) != "" {
				segments = append(segments, ThinkingTextSegment{Text: before, IsThinking: false})
			}
		}

		afterStart := remaining[startPos+len(thinkingStartTag):]

		endPos := FindThinkingEndTag(afterStart)
		if endPos < 0 {
			endPos = FindThinkingEndTagAtEnd(afterStart)
		}
		if endPos < 0 {
			thinkingContent := afterStart
			if strings.HasPrefix(thinkingContent, "\n") {
				thinkingContent = thinkingContent[1:]
			}
			segments = append(segments, ThinkingTextSegment{Text: thinkingContent, IsThinking: true})
			break
		}

		thinkingContent := afterStart[:endPos]
		if strings.HasPrefix(thinkingContent, "\n") {
			thinkingContent = thinkingContent[1:]
		}
		segments = append(segments, ThinkingTextSegment{Text: thinkingContent, IsThinking: true})

		afterEnd := afterStart[endPos+len(thinkingEndTag):]
		if strings.HasPrefix(afterEnd, "\n\n") {
			afterEnd = afterEnd[2:]
		}
		remaining = afterEnd
	}

	return segments
}

// ThinkingTextSegment represents a segment of text that is either thinking or regular text.
type ThinkingTextSegment struct {
	Text       string
	IsThinking bool
}

// IsThinkingEnabledInAntigravityRequest checks whether thinking is enabled
// in an Antigravity-format request body.
func IsThinkingEnabledInAntigravityRequest(requestJSON []byte) bool {
	if inc := gjson.GetBytes(requestJSON, "request.generationConfig.thinkingConfig.includeThoughts"); inc.Exists() {
		return inc.Bool()
	}
	if inc := gjson.GetBytes(requestJSON, "request.generationConfig.thinkingConfig.include_thoughts"); inc.Exists() {
		return inc.Bool()
	}
	if budget := gjson.GetBytes(requestJSON, "request.generationConfig.thinkingConfig.thinkingBudget"); budget.Exists() {
		return budget.Int() > 0 || budget.Int() == -1
	}
	if budget := gjson.GetBytes(requestJSON, "request.generationConfig.thinkingConfig.thinking_budget"); budget.Exists() {
		return budget.Int() > 0 || budget.Int() == -1
	}
	if level := gjson.GetBytes(requestJSON, "request.generationConfig.thinkingConfig.thinkingLevel"); level.Exists() {
		return level.String() != "" && level.String() != "THINKING_LEVEL_NONE"
	}
	if level := gjson.GetBytes(requestJSON, "request.generationConfig.thinkingConfig.thinking_level"); level.Exists() {
		return level.String() != "" && level.String() != "THINKING_LEVEL_NONE"
	}
	return false
}

// SyntheticThinkingSignature generates a deterministic placeholder signature
// for thinking blocks extracted from XML tags (which lack a real cryptographic signature).
func SyntheticThinkingSignature() string {
	return "cliproxy-synthetic-thinking-signature"
}
