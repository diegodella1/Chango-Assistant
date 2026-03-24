package agent

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// formatMessagesForLog formats messages for logging using strings.Builder.
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var b strings.Builder
	b.WriteString("[\n")
	for i, msg := range messages {
		fmt.Fprintf(&b, "  [%d] Role: %s\n", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			b.WriteString("  ToolCalls:\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&b, "    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					fmt.Fprintf(&b, "      Arguments: %s\n", utils.Truncate(tc.Function.Arguments, 200))
				}
			}
		}
		if msg.Content != "" {
			fmt.Fprintf(&b, "  Content: %s\n", utils.Truncate(msg.Content, 200))
		}
		if msg.ToolCallID != "" {
			fmt.Fprintf(&b, "  ToolCallID: %s\n", msg.ToolCallID)
		}
		b.WriteByte('\n')
	}
	b.WriteByte(']')
	return b.String()
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(tools []providers.ToolDefinition) string {
	if len(tools) == 0 {
		return "[]"
	}

	var result string
	result += "[\n"
	for i, tool := range tools {
		result += fmt.Sprintf("  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		result += fmt.Sprintf("      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			result += fmt.Sprintf("      Parameters: %s\n", utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200))
		}
	}
	result += "]"
	return result
}
