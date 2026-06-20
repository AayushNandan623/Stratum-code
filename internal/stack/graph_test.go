package stack

import (
	"testing"

	"github.com/google/uuid"
)

func uid() uuid.UUID { return uuid.New() }

// dep builds a Dependency edge a → b (a depends on b).
func dep(a, b uuid.UUID) *Dependency {
	return &Dependency{StackID: a, DependsOnID: b}
}

func TestHasCycle_NoCycle(t *testing.T) {
	a, b, c := uid(), uid(), uid()
	deps := []*Dependency{dep(a, b), dep(b, c)}
	adj := buildAdjacencyWithEdge(deps, DependencyEdge{})
	if hasCycle(adj, a) {
		t.Fatal("expected no cycle in a linear chain a→b→c")
	}
}

func TestHasCycle_SimpleCycle(t *testing.T) {
	a, b := uid(), uid()
	// a→b already exists; propose b→a, which closes a cycle.
	deps := []*Dependency{dep(a, b)}
	edge := DependencyEdge{StackID: b, DependsOnID: a}
	adj := buildAdjacencyWithEdge(deps, edge)
	if !hasCycle(adj, edge.StackID) {
		t.Fatal("expected cycle when b→a is added to a→b")
	}
}

func TestHasCycle_SelfLoop(t *testing.T) {
	a := uid()
	edge := DependencyEdge{StackID: a, DependsOnID: a}
	adj := buildAdjacencyWithEdge(nil, edge)
	if !hasCycle(adj, edge.StackID) {
		t.Fatal("expected cycle for a self-dependency")
	}
}

func TestHasCycle_MultiNodeCycle(t *testing.T) {
	a, b, c, d := uid(), uid(), uid(), uid()
	// a→b→c→d exists; propose d→a to close a 4-node cycle.
	deps := []*Dependency{dep(a, b), dep(b, c), dep(c, d)}
	edge := DependencyEdge{StackID: d, DependsOnID: a}
	adj := buildAdjacencyWithEdge(deps, edge)
	if !hasCycle(adj, edge.StackID) {
		t.Fatal("expected cycle when d→a closes a→b→c→d→a")
	}
}

func TestHasCycle_ProposedEdgeDoesNotCloseCycle(t *testing.T) {
	a, b, c, d := uid(), uid(), uid(), uid()
	// a→b→c and a→d (a diamond). Proposing c→d must NOT be a cycle.
	deps := []*Dependency{dep(a, b), dep(b, c), dep(a, d)}
	edge := DependencyEdge{StackID: c, DependsOnID: d}
	adj := buildAdjacencyWithEdge(deps, edge)
	if hasCycle(adj, edge.StackID) {
		t.Fatal("expected no cycle when adding c→d to a diamond a→b→c, a→d")
	}
}

func TestHasCycle_DisconnectedNoCycle(t *testing.T) {
	a, b, c, d := uid(), uid(), uid(), uid()
	// Two separate chains a→b and c→d; proposing b→c connects them without a cycle.
	deps := []*Dependency{dep(a, b), dep(c, d)}
	edge := DependencyEdge{StackID: b, DependsOnID: c}
	adj := buildAdjacencyWithEdge(deps, edge)
	if hasCycle(adj, edge.StackID) {
		t.Fatal("expected no cycle when connecting two disjoint chains with b→c")
	}
}

func TestHasCycle_EmptyGraph(t *testing.T) {
	a, b := uid(), uid()
	edge := DependencyEdge{StackID: a, DependsOnID: b}
	adj := buildAdjacencyWithEdge(nil, edge)
	if hasCycle(adj, edge.StackID) {
		t.Fatal("expected no cycle in a graph with a single edge")
	}
}
