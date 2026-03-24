package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// KnowledgeNode represents an entity in the knowledge graph.
type KnowledgeNode struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`       // person, project, concept, service, company
	Name       string            `json:"name"`
	Properties map[string]string `json:"properties"` // key-value pairs
}

// KnowledgeEdge represents a relationship between two nodes.
type KnowledgeEdge struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	Relation string  `json:"relation"` // works_at, competes_with, uses, knows, depends_on, etc.
	Weight   float64 `json:"weight"`   // confidence 0-1
	Source   string  `json:"source"`   // how we learned this
}

// KnowledgeGraph holds all nodes and edges.
type KnowledgeGraph struct {
	Nodes []KnowledgeNode `json:"nodes"`
	Edges []KnowledgeEdge `json:"edges"`
}

// KnowledgeGraphTool provides LLM-accessible graph operations.
type KnowledgeGraphTool struct {
	filePath string
	mu       sync.Mutex
}

// NewKnowledgeGraphTool creates a new knowledge graph tool.
func NewKnowledgeGraphTool(workspace string) *KnowledgeGraphTool {
	dir := filepath.Join(workspace, "state")
	os.MkdirAll(dir, 0755)
	return &KnowledgeGraphTool{
		filePath: filepath.Join(dir, "knowledge_graph.json"),
	}
}

func (t *KnowledgeGraphTool) Name() string { return "knowledge_graph" }

func (t *KnowledgeGraphTool) Description() string {
	return "Knowledge graph for tracking entities and relationships. Add nodes (people, projects, concepts, services, companies) and edges (relationships) between them. Query connections, search, find paths, and visualize the graph."
}

func (t *KnowledgeGraphTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"add_node", "add_edge", "query", "search", "path", "visualize", "remove_node", "remove_edge"},
				"description": "Action to perform",
			},
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Node ID (slug). Required for add_node, query, remove_node.",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"person", "project", "concept", "service", "company", "place", "tool"},
				"description": "Node type (for add_node)",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Human-readable name (for add_node)",
			},
			"properties": map[string]interface{}{
				"type":        "object",
				"description": "Key-value properties for the node (for add_node)",
			},
			"from": map[string]interface{}{
				"type":        "string",
				"description": "Source node ID (for add_edge, path, remove_edge)",
			},
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Target node ID (for add_edge, path, remove_edge)",
			},
			"relation": map[string]interface{}{
				"type":        "string",
				"description": "Relationship type (for add_edge, remove_edge): works_at, competes_with, uses, knows, depends_on, etc.",
			},
			"weight": map[string]interface{}{
				"type":        "number",
				"description": "Confidence weight 0-1 (for add_edge, default 1.0)",
			},
			"source": map[string]interface{}{
				"type":        "string",
				"description": "How we learned this relationship (for add_edge)",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query string (for search action)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *KnowledgeGraphTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)

	switch action {
	case "add_node":
		return t.addNode(args)
	case "add_edge":
		return t.addEdge(args)
	case "query":
		return t.queryNode(args)
	case "search":
		return t.searchNodes(args)
	case "path":
		return t.findPath(args)
	case "visualize":
		return t.visualize()
	case "remove_node":
		return t.removeNode(args)
	case "remove_edge":
		return t.removeEdge(args)
	default:
		return ErrorResult(fmt.Sprintf("Unknown action: %s. Valid: add_node, add_edge, query, search, path, visualize, remove_node, remove_edge", action))
	}
}

func (t *KnowledgeGraphTool) load() (*KnowledgeGraph, error) {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &KnowledgeGraph{
				Nodes: []KnowledgeNode{},
				Edges: []KnowledgeEdge{},
			}, nil
		}
		return nil, err
	}
	var g KnowledgeGraph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	if g.Nodes == nil {
		g.Nodes = []KnowledgeNode{}
	}
	if g.Edges == nil {
		g.Edges = []KnowledgeEdge{}
	}
	return &g, nil
}

func (t *KnowledgeGraphTool) save(g *KnowledgeGraph) error {
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, t.filePath)
}

func (t *KnowledgeGraphTool) addNode(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	nodeType, _ := args["type"].(string)
	name, _ := args["name"].(string)

	if id == "" || nodeType == "" || name == "" {
		return ErrorResult("add_node requires id, type, and name")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	// Check if node exists — update it
	for i, n := range g.Nodes {
		if n.ID == id {
			g.Nodes[i].Type = nodeType
			g.Nodes[i].Name = name
			if props, ok := args["properties"].(map[string]interface{}); ok {
				g.Nodes[i].Properties = toStringMap(props)
			}
			if err := t.save(g); err != nil {
				return ErrorResult(fmt.Sprintf("Failed to save: %v", err))
			}
			return SilentResult(fmt.Sprintf("Updated node '%s' (%s: %s)", id, nodeType, name))
		}
	}

	node := KnowledgeNode{
		ID:   id,
		Type: nodeType,
		Name: name,
	}
	if props, ok := args["properties"].(map[string]interface{}); ok {
		node.Properties = toStringMap(props)
	} else {
		node.Properties = map[string]string{}
	}

	g.Nodes = append(g.Nodes, node)
	if err := t.save(g); err != nil {
		return ErrorResult(fmt.Sprintf("Failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Added node '%s' (%s: %s). Graph now has %d nodes.", id, nodeType, name, len(g.Nodes)))
}

func (t *KnowledgeGraphTool) addEdge(args map[string]interface{}) *ToolResult {
	from, _ := args["from"].(string)
	to, _ := args["to"].(string)
	relation, _ := args["relation"].(string)

	if from == "" || to == "" || relation == "" {
		return ErrorResult("add_edge requires from, to, and relation")
	}

	weight := 1.0
	if w, ok := args["weight"].(float64); ok {
		weight = w
	}
	source, _ := args["source"].(string)

	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	// Verify both nodes exist
	fromExists, toExists := false, false
	for _, n := range g.Nodes {
		if n.ID == from {
			fromExists = true
		}
		if n.ID == to {
			toExists = true
		}
	}
	if !fromExists {
		return ErrorResult(fmt.Sprintf("Node '%s' not found. Add it first with add_node.", from))
	}
	if !toExists {
		return ErrorResult(fmt.Sprintf("Node '%s' not found. Add it first with add_node.", to))
	}

	// Check for existing edge — update it
	for i, e := range g.Edges {
		if e.From == from && e.To == to && e.Relation == relation {
			g.Edges[i].Weight = weight
			if source != "" {
				g.Edges[i].Source = source
			}
			if err := t.save(g); err != nil {
				return ErrorResult(fmt.Sprintf("Failed to save: %v", err))
			}
			return SilentResult(fmt.Sprintf("Updated edge %s -[%s]-> %s (weight: %.2f)", from, relation, to, weight))
		}
	}

	edge := KnowledgeEdge{
		From:     from,
		To:       to,
		Relation: relation,
		Weight:   weight,
		Source:   source,
	}
	g.Edges = append(g.Edges, edge)
	if err := t.save(g); err != nil {
		return ErrorResult(fmt.Sprintf("Failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Added edge %s -[%s]-> %s (weight: %.2f). Graph now has %d edges.", from, relation, to, weight, len(g.Edges)))
}

func (t *KnowledgeGraphTool) queryNode(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("query requires id")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	// Find node
	var node *KnowledgeNode
	for _, n := range g.Nodes {
		if n.ID == id {
			n2 := n
			node = &n2
			break
		}
	}
	if node == nil {
		return ErrorResult(fmt.Sprintf("Node '%s' not found", id))
	}

	// Find connected edges and neighbors
	var edges []KnowledgeEdge
	neighborIDs := map[string]bool{}
	for _, e := range g.Edges {
		if e.From == id || e.To == id {
			edges = append(edges, e)
			if e.From == id {
				neighborIDs[e.To] = true
			} else {
				neighborIDs[e.From] = true
			}
		}
	}

	var neighbors []KnowledgeNode
	for _, n := range g.Nodes {
		if neighborIDs[n.ID] {
			neighbors = append(neighbors, n)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Node: %s (%s)\nType: %s\n", node.Name, node.ID, node.Type))
	if len(node.Properties) > 0 {
		sb.WriteString("Properties:\n")
		for k, v := range node.Properties {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}
	if len(edges) > 0 {
		sb.WriteString(fmt.Sprintf("\nConnections (%d):\n", len(edges)))
		for _, e := range edges {
			if e.From == id {
				sb.WriteString(fmt.Sprintf("  -> %s [%s] (weight: %.2f)\n", e.To, e.Relation, e.Weight))
			} else {
				sb.WriteString(fmt.Sprintf("  <- %s [%s] (weight: %.2f)\n", e.From, e.Relation, e.Weight))
			}
		}
	}
	if len(neighbors) > 0 {
		sb.WriteString(fmt.Sprintf("\nNeighbors (%d):\n", len(neighbors)))
		for _, n := range neighbors {
			sb.WriteString(fmt.Sprintf("  %s (%s) - %s\n", n.Name, n.ID, n.Type))
		}
	}

	return SilentResult(sb.String())
}

func (t *KnowledgeGraphTool) searchNodes(args map[string]interface{}) *ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ErrorResult("search requires query")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	queryLower := strings.ToLower(query)
	var matches []KnowledgeNode

	for _, n := range g.Nodes {
		if strings.Contains(strings.ToLower(n.ID), queryLower) ||
			strings.Contains(strings.ToLower(n.Name), queryLower) ||
			strings.Contains(strings.ToLower(n.Type), queryLower) {
			matches = append(matches, n)
			continue
		}
		// Search properties
		for _, v := range n.Properties {
			if strings.Contains(strings.ToLower(v), queryLower) {
				matches = append(matches, n)
				break
			}
		}
	}

	if len(matches) == 0 {
		return SilentResult(fmt.Sprintf("No nodes found matching '%s'", query))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d nodes matching '%s':\n", len(matches), query))
	for _, n := range matches {
		sb.WriteString(fmt.Sprintf("  [%s] %s (%s)\n", n.Type, n.Name, n.ID))
	}
	return SilentResult(sb.String())
}

func (t *KnowledgeGraphTool) findPath(args map[string]interface{}) *ToolResult {
	from, _ := args["from"].(string)
	to, _ := args["to"].(string)

	if from == "" || to == "" {
		return ErrorResult("path requires from and to")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	// BFS
	type bfsState struct {
		nodeID string
		path   []string
		edges  []string // relation labels along the path
	}

	visited := map[string]bool{}
	queue := []bfsState{{nodeID: from, path: []string{from}, edges: []string{}}}
	visited[from] = true

	// Build adjacency list (undirected)
	adj := map[string][]struct {
		neighbor string
		relation string
	}{}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], struct {
			neighbor string
			relation string
		}{e.To, e.Relation})
		adj[e.To] = append(adj[e.To], struct {
			neighbor string
			relation string
		}{e.From, e.Relation})
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current.nodeID == to {
			// Found path
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Path from '%s' to '%s' (%d hops):\n", from, to, len(current.edges)))
			for i, nodeID := range current.path {
				// Find node name
				name := nodeID
				for _, n := range g.Nodes {
					if n.ID == nodeID {
						name = n.Name
						break
					}
				}
				if i < len(current.edges) {
					sb.WriteString(fmt.Sprintf("  %s -[%s]-> ", name, current.edges[i]))
				} else {
					sb.WriteString(fmt.Sprintf("  %s\n", name))
				}
			}
			return SilentResult(sb.String())
		}

		for _, neighbor := range adj[current.nodeID] {
			if !visited[neighbor.neighbor] {
				visited[neighbor.neighbor] = true
				newPath := make([]string, len(current.path))
				copy(newPath, current.path)
				newPath = append(newPath, neighbor.neighbor)
				newEdges := make([]string, len(current.edges))
				copy(newEdges, current.edges)
				newEdges = append(newEdges, neighbor.relation)
				queue = append(queue, bfsState{
					nodeID: neighbor.neighbor,
					path:   newPath,
					edges:  newEdges,
				})
			}
		}
	}

	return SilentResult(fmt.Sprintf("No path found between '%s' and '%s'", from, to))
}

func (t *KnowledgeGraphTool) visualize() *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	if len(g.Nodes) == 0 {
		return SilentResult("Knowledge graph is empty. Use add_node to start building it.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Knowledge Graph: %d nodes, %d edges\n", len(g.Nodes), len(g.Edges)))
	sb.WriteString("===\n\n")

	// Group nodes by type
	byType := map[string][]KnowledgeNode{}
	for _, n := range g.Nodes {
		byType[n.Type] = append(byType[n.Type], n)
	}

	for nodeType, nodes := range byType {
		sb.WriteString(fmt.Sprintf("[%s] (%d)\n", strings.ToUpper(nodeType), len(nodes)))
		for _, n := range nodes {
			sb.WriteString(fmt.Sprintf("  - %s (%s)", n.Name, n.ID))
			if len(n.Properties) > 0 {
				props := []string{}
				for k, v := range n.Properties {
					props = append(props, fmt.Sprintf("%s=%s", k, v))
				}
				sb.WriteString(fmt.Sprintf(" {%s}", strings.Join(props, ", ")))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(g.Edges) > 0 {
		sb.WriteString("Relationships:\n")
		for _, e := range g.Edges {
			weightStr := ""
			if e.Weight < 1.0 {
				weightStr = fmt.Sprintf(" (%.0f%%)", e.Weight*100)
			}
			sb.WriteString(fmt.Sprintf("  %s -[%s]-> %s%s\n", e.From, e.Relation, e.To, weightStr))
		}
	}

	return SilentResult(sb.String())
}

func (t *KnowledgeGraphTool) removeNode(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("remove_node requires id")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	found := false
	newNodes := make([]KnowledgeNode, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.ID == id {
			found = true
			continue
		}
		newNodes = append(newNodes, n)
	}
	if !found {
		return ErrorResult(fmt.Sprintf("Node '%s' not found", id))
	}

	// Remove edges involving this node
	removedEdges := 0
	newEdges := make([]KnowledgeEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if e.From == id || e.To == id {
			removedEdges++
			continue
		}
		newEdges = append(newEdges, e)
	}

	g.Nodes = newNodes
	g.Edges = newEdges
	if err := t.save(g); err != nil {
		return ErrorResult(fmt.Sprintf("Failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Removed node '%s' and %d connected edges", id, removedEdges))
}

func (t *KnowledgeGraphTool) removeEdge(args map[string]interface{}) *ToolResult {
	from, _ := args["from"].(string)
	to, _ := args["to"].(string)
	relation, _ := args["relation"].(string)

	if from == "" || to == "" {
		return ErrorResult("remove_edge requires from and to")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	g, err := t.load()
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to load graph: %v", err))
	}

	found := false
	newEdges := make([]KnowledgeEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if e.From == from && e.To == to && (relation == "" || e.Relation == relation) {
			found = true
			continue
		}
		newEdges = append(newEdges, e)
	}
	if !found {
		return ErrorResult(fmt.Sprintf("Edge from '%s' to '%s' not found", from, to))
	}

	g.Edges = newEdges
	if err := t.save(g); err != nil {
		return ErrorResult(fmt.Sprintf("Failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Removed edge %s -> %s", from, to))
}

// toStringMap converts a map[string]interface{} to map[string]string.
func toStringMap(m map[string]interface{}) map[string]string {
	result := map[string]string{}
	for k, v := range m {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}
