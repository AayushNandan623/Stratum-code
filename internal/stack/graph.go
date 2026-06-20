// Stack dependency graph: cycle detection via DFS coloring. The graph is small
// (hundreds of stacks per org), so an in-memory DFS per write is correct and
// fast. This is pure logic with no I/O, making it straightforward to test.
package stack

import "github.com/google/uuid"

// dfs color states.
const (
	colorWhite color = iota // unvisited
	colorGray               // on the current DFS path
	colorBlack              // fully explored
)

type color int

// hasCycle reports whether the adjacency list contains a cycle reachable from
// start. A back edge to a GRAY node indicates a cycle. Starting from the
// proposed edge's source is sufficient: any cycle involving the new edge must
// pass through that source.
func hasCycle(adj map[uuid.UUID][]uuid.UUID, start uuid.UUID) bool {
	colors := make(map[uuid.UUID]color)
	var dfs func(node uuid.UUID) bool
	dfs = func(node uuid.UUID) bool {
		colors[node] = colorGray
		for _, neighbor := range adj[node] {
			switch colors[neighbor] {
			case colorGray:
				return true // back edge → cycle
			case colorWhite:
				if dfs(neighbor) {
					return true
				}
			}
			// colorBlack: already fully explored; skip.
		}
		colors[node] = colorBlack
		return false
	}
	return dfs(start)
}

// buildAdjacencyWithEdge constructs the org-wide "depends on" adjacency list
// (StackID → []DependsOnID) with the proposed edge temporarily added.
func buildAdjacencyWithEdge(deps []*Dependency, edge DependencyEdge) map[uuid.UUID][]uuid.UUID {
	adj := make(map[uuid.UUID][]uuid.UUID)
	for _, d := range deps {
		adj[d.StackID] = append(adj[d.StackID], d.DependsOnID)
	}
	adj[edge.StackID] = append(adj[edge.StackID], edge.DependsOnID)
	return adj
}
