package agent

import (
	"fmt"
	"strings"
)

// findAnalogies searches the knowledge graph and memory vault for situations
// similar to the current message. Returns context to inject as a system message.
// Zero LLM cost — pure graph traversal + text matching.
func (al *AgentLoop) findAnalogies(userMessage string) string {
	tokens := simpleTokenize(userMessage)
	if len(tokens) == 0 {
		return ""
	}

	var parts []string
	seen := map[string]bool{} // deduplicate entries

	// 1. Search the knowledge graph for matching nodes
	if al.knowledgeGraph != nil {
		nodes := al.knowledgeGraph.SearchNodes(userMessage, 5)
		for _, node := range nodes {
			// Get 1-hop neighbors via edges
			_, edges := al.knowledgeGraph.QueryNode(node.ID)
			if len(edges) == 0 {
				// Isolated node — still useful as context
				entry := fmt.Sprintf("- Related entity: %s (%s)", node.Name, node.Type)
				if len(node.Properties) > 0 {
					props := make([]string, 0, len(node.Properties))
					for k, v := range node.Properties {
						props = append(props, fmt.Sprintf("%s=%s", k, v))
					}
					entry += " — " + strings.Join(props, ", ")
				}
				if !seen[node.ID] {
					parts = append(parts, entry)
					seen[node.ID] = true
				}
				continue
			}

			// Node with connections — show relationships
			for _, edge := range edges {
				var neighborID string
				var direction string
				if edge.From == node.ID {
					neighborID = edge.To
					direction = "->"
				} else {
					neighborID = edge.From
					direction = "<-"
				}
				neighborName := al.knowledgeGraph.GetNodeName(neighborID)
				key := node.ID + direction + neighborID + edge.Relation
				if seen[key] {
					continue
				}
				seen[key] = true

				entry := fmt.Sprintf("- %s %s [%s] %s %s", node.Name, direction, edge.Relation, neighborName, "")
				if edge.Source != "" {
					entry = fmt.Sprintf("- %s %s [%s] %s (source: %s)", node.Name, direction, edge.Relation, neighborName, edge.Source)
				}
				parts = append(parts, strings.TrimSpace(entry))
			}
		}
	}

	// 2. Search memory vault for matching notes
	if al.memoryTool != nil {
		notes := al.memoryTool.SearchNotes(userMessage, 3)
		for _, note := range notes {
			key := "memo:" + note.Key
			if seen[key] {
				continue
			}
			seen[key] = true
			// Truncate note content to keep injection compact
			content := note.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			parts = append(parts, fmt.Sprintf("- Memory [%s]: %s", note.Key, content))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	// Build the analogical context block, capped at 1000 chars
	header := "## Past Experience (from knowledge graph & memory)\n"
	result := header + strings.Join(parts, "\n")
	if len(result) > 1000 {
		result = result[:997] + "..."
	}
	return result
}
