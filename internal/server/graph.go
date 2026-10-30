package server

import "wtable/internal/model"

// snapshotGraph derives presentation metadata without changing persisted grants.
func snapshotGraph(snapshot model.Snapshot) *model.Graph {
	graph := &model.Graph{
		Nodes: []model.GraphNode{{ID: "you", Name: "你", State: "running", Attention: []string{}}},
		Edges: []model.GraphEdge{},
	}
	indices := map[string]int{"you": 0}
	for _, pane := range snapshot.Panes {
		node := model.GraphNode{
			ID: pane.ID, Name: pane.Name, Preset: pane.Preset, Backend: pane.Backend,
			Layer: pane.Layer, State: pane.State, Attention: []string{},
		}
		if pane.State == "exited" || pane.State == "failed" || pane.State == "interrupted" {
			node.Attention = append(node.Attention, "exited")
		}
		if pane.TurnState == "waiting-permission" {
			node.Attention = append(node.Attention, "needs-approval")
		}
		indices[node.ID] = len(graph.Nodes)
		graph.Nodes = append(graph.Nodes, node)
		graph.Edges = append(graph.Edges, model.GraphEdge{From: "you", To: pane.ID, Kind: "virtual"})
	}
	for _, permission := range snapshot.Permissions {
		if i, ok := indices[permission.PaneID]; ok && !contains(graph.Nodes[i].Attention, "needs-approval") {
			graph.Nodes[i].Attention = append(graph.Nodes[i].Attention, "needs-approval")
		}
	}
	for _, edge := range snapshot.Edges {
		graph.Edges = append(graph.Edges, model.GraphEdge{From: edge.From, To: edge.To, Kind: "grant"})
	}
	// Unread belongs to the inbox recipient, never both participants.
	markUnread := func(to string) {
		if to == "controller" {
			to = "you"
		}
		if i, ok := indices[to]; ok {
			graph.Nodes[i].Unread++
			if !contains(graph.Nodes[i].Attention, "bell") {
				graph.Nodes[i].Attention = append(graph.Nodes[i].Attention, "bell")
			}
		}
	}
	active := map[model.Edge]int{}
	for _, task := range snapshot.Tasks {
		from := task.From
		if from == "controller" {
			from = "you"
		}
		if activeState(task.State) {
			active[model.Edge{From: from, To: task.To}]++
		}
		if !task.Seen && contains([]string{"completed", "failed", "unknown"}, task.State) {
			markUnread(task.To)
		}
	}
	for _, report := range snapshot.Reports {
		if !report.Seen {
			markUnread(report.To)
		}
	}
	for i := range graph.Edges {
		edge := &graph.Edges[i]
		edge.Active = active[model.Edge{From: edge.From, To: edge.To}]
	}
	return graph
}
