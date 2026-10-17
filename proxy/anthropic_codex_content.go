package proxy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var claudeCodeModelBannerPattern = regexp.MustCompile(`(?i)^You are powered by the model[ \t]+\S+\.?$`)

func isClaudeCodeTransportMetadata(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) > clientScaffoldingMaxNormalizeBytes || strings.ContainsAny(text, "\r\n") {
		return false
	}
	if claudeCodeModelBannerPattern.MatchString(text) {
		return true
	}
	if !strings.HasPrefix(text, claudeBillingHeaderPrefix) || !claudeBillingVersionPattern.MatchString(text) {
		return false
	}
	for _, field := range strings.Split(strings.TrimPrefix(text, claudeBillingHeaderPrefix), ";") {
		if field = strings.TrimSpace(field); field == "" {
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" || strings.ContainsAny(key, " \t") || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func stripClaudeCodeSystemMetadata(text string) string {
	// Only leading transport lines are removable; quoted examples and task text stay intact.
	for {
		line, rest, more := strings.Cut(text, "\n")
		if !isClaudeCodeTransportMetadata(line) {
			return text
		}
		if !more {
			return ""
		}
		text = rest
	}
}

func normalizeClaudeCodeTransportReminder(text string) string {
	if len(text) > clientScaffoldingMaxNormalizeBytes {
		return text
	}
	match := clientScaffoldingReminderBlockPattern.FindStringSubmatch(strings.TrimSpace(text))
	if len(match) != 2 || match[0] != strings.TrimSpace(text) {
		return text
	}
	cleaned := stripClaudeCodeSystemMetadata(match[1])
	if strings.TrimSpace(cleaned) == "" {
		return ""
	}
	return strings.Replace(text, match[1], cleaned, 1)
}

func validateAnthropicCodexContent(messages []anthropicMessage) error {
	for i, message := range messages {
		switch message.Role {
		case "user", "assistant", "system", "developer":
		default:
			return fmt.Errorf("messages[%d]: unsupported role %q for the Codex mapping", i, message.Role)
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil && string(message.Content) != "null" {
			continue
		}
		var blocks []anthropicContentBlock
		if json.Unmarshal(message.Content, &blocks) != nil || string(message.Content) == "null" {
			return fmt.Errorf("messages[%d].content must be text or an array of content blocks", i)
		}
		if err := validateAnthropicCodexBlocks(blocks, message.Role); err != nil {
			return fmt.Errorf("messages[%d]: %w", i, err)
		}
	}
	return nil
}

func validateAnthropicCodexBlocks(blocks []anthropicContentBlock, role string) error {
	for i, block := range blocks {
		allowed := false
		switch block.Type {
		case "text":
			allowed = true
		case "image", "document":
			allowed = role == "user" || role == "tool_result"
		case "tool_result":
			allowed = role == "user"
		case "tool_use", "thinking", "server_tool_use", "web_search_tool_result":
			allowed = role == "assistant"
		}
		if !allowed {
			return fmt.Errorf("content[%d]: unsupported content block for the Codex mapping", i)
		}
		if block.Type == "image" && (block.Source == nil || block.Source.Type != "base64" || strings.TrimSpace(block.Source.MediaType) == "" || strings.TrimSpace(block.Source.Data) == "") {
			return fmt.Errorf("content[%d]: image requires a base64 source with media_type and non-empty data; URL images are not supported by this mapping", i)
		}
		if block.Type == "document" && (block.Source == nil || block.Source.Type != "text" || (block.Source.MediaType != "" && block.Source.MediaType != "text/plain")) {
			return fmt.Errorf("content[%d]: document requires a text/plain source with type=text; binary and URL documents are not supported by this mapping", i)
		}
		if block.Type == "tool_result" {
			var text string
			if len(block.Content) == 0 || string(block.Content) == "null" || json.Unmarshal(block.Content, &text) == nil {
				continue
			}
			var nested []anthropicContentBlock
			if json.Unmarshal(block.Content, &nested) != nil {
				return fmt.Errorf("content[%d]: tool_result content must be text or an array of blocks", i)
			}
			if err := validateAnthropicCodexBlocks(nested, "tool_result"); err != nil {
				return fmt.Errorf("content[%d].tool_result: %w", i, err)
			}
		}
	}
	return nil
}

func anthropicTextDocument(block anthropicContentBlock) string {
	var parts []string
	if block.Title != "" {
		parts = append(parts, "Document title: "+block.Title)
	}
	if block.Context != "" {
		parts = append(parts, "Document context: "+block.Context)
	}
	if block.Source != nil {
		parts = append(parts, block.Source.Data)
	}
	return strings.Join(parts, "\n\n")
}
