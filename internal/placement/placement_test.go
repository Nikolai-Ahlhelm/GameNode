package placement

import "testing"

func TestDecideSelectsMostSpareCapacity(t *testing.T) {
	req := Request{
		TenantID:             "tenant-a",
		RequiredCapabilities: []string{"native_runtime"},
		Candidates: []NodeCandidate{
			{NodeID: "local", DisplayName: "Local", Kind: NodeLocal, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime", "container_runtime"}, CapacityKnown: true, UsedServers: 45, MaxServers: 50},
			{NodeID: "node-b", DisplayName: "Node B", Kind: NodeRemote, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}},
		},
	}
	got := Decide(req)
	if got.Rejected {
		t.Fatalf("expected a selection, got rejected: %+v", got)
	}
	if got.Selected == nil || got.Selected.NodeID != "local" {
		t.Fatalf("expected local to be selected (capacity-known beats capacity-unknown), got %+v", got.Selected)
	}
	if got.Execution != ExecutionLocalOnly {
		t.Fatalf("expected local_only execution, got %q", got.Execution)
	}
}

func TestDecidePrefersMoreHeadroomAmongCapacityKnown(t *testing.T) {
	req := Request{
		Candidates: []NodeCandidate{
			{NodeID: "a", Kind: NodeLocal, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}, CapacityKnown: true, UsedServers: 40, MaxServers: 50},
			{NodeID: "b", Kind: NodeLocal, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}, CapacityKnown: true, UsedServers: 10, MaxServers: 50},
		},
	}
	got := Decide(req)
	if got.Selected == nil || got.Selected.NodeID != "b" {
		t.Fatalf("expected node b (more headroom: 40 vs 10) to be selected, got %+v", got.Selected)
	}
}

func TestDecideDeterministicTieBreakByNodeID(t *testing.T) {
	req := Request{
		Candidates: []NodeCandidate{
			{NodeID: "zzz", Kind: NodeRemote, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}},
			{NodeID: "aaa", Kind: NodeRemote, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}},
		},
	}
	for i := 0; i < 5; i++ {
		got := Decide(req)
		if got.Selected == nil || got.Selected.NodeID != "aaa" {
			t.Fatalf("expected deterministic tie-break to select 'aaa', got %+v (iteration %d)", got.Selected, i)
		}
	}
}

func TestDecideExcludesDisabledNode(t *testing.T) {
	req := Request{
		Candidates: []NodeCandidate{
			{NodeID: "disabled-node", Kind: NodeRemote, Enabled: false, Healthy: true, Capabilities: []string{"native_runtime"}},
		},
	}
	got := Decide(req)
	if !got.Rejected || got.Reason != ReasonNoEligibleNode {
		t.Fatalf("expected rejection with no_eligible_node, got %+v", got)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Reason != ReasonDisabled {
		t.Fatalf("expected candidate reason 'disabled', got %+v", got.Candidates)
	}
}

func TestDecideExcludesUnhealthyRemoteNode(t *testing.T) {
	req := Request{
		Candidates: []NodeCandidate{
			{NodeID: "unreachable-node", Kind: NodeRemote, Enabled: true, Healthy: false, Capabilities: []string{"native_runtime"}},
		},
	}
	got := Decide(req)
	if !got.Rejected {
		t.Fatalf("expected rejection, got %+v", got)
	}
	if got.Candidates[0].Reason != ReasonUnhealthy {
		t.Fatalf("expected reason 'unhealthy', got %q", got.Candidates[0].Reason)
	}
}

func TestDecideExcludesMissingCapability(t *testing.T) {
	req := Request{
		RequiredCapabilities: []string{"container_runtime"},
		Candidates: []NodeCandidate{
			{NodeID: "native-only", Kind: NodeLocal, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}, CapacityKnown: true, UsedServers: 0, MaxServers: 10},
		},
	}
	got := Decide(req)
	if !got.Rejected {
		t.Fatalf("expected rejection, got %+v", got)
	}
	if got.Candidates[0].Reason != ReasonMissingCapability {
		t.Fatalf("expected reason 'missing_capability', got %q", got.Candidates[0].Reason)
	}
}

func TestDecideExcludesCapacityExhausted(t *testing.T) {
	req := Request{
		Candidates: []NodeCandidate{
			{NodeID: "full", Kind: NodeLocal, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}, CapacityKnown: true, UsedServers: 50, MaxServers: 50},
		},
	}
	got := Decide(req)
	if !got.Rejected {
		t.Fatalf("expected rejection, got %+v", got)
	}
	if got.Candidates[0].Reason != ReasonCapacityExhausted {
		t.Fatalf("expected reason 'capacity_exhausted', got %q", got.Candidates[0].Reason)
	}
}

func TestDecideRemoteSelectionRequiresV05B(t *testing.T) {
	req := Request{
		Candidates: []NodeCandidate{
			{NodeID: "node-only", Kind: NodeRemote, Enabled: true, Healthy: true, Capabilities: []string{"native_runtime"}},
		},
	}
	got := Decide(req)
	if got.Rejected {
		t.Fatalf("expected a selection, got rejected: %+v", got)
	}
	if got.Selected.Kind != NodeRemote {
		t.Fatalf("expected the remote node to be selected, got %+v", got.Selected)
	}
	if got.Execution != ExecutionRequiresV05B {
		t.Fatalf("expected execution requires_v0.5b, got %q", got.Execution)
	}
}

func TestDecideNoCandidatesRejected(t *testing.T) {
	got := Decide(Request{})
	if !got.Rejected || got.Reason != ReasonNoEligibleNode {
		t.Fatalf("expected rejection with no_eligible_node for empty candidate list, got %+v", got)
	}
	if got.Selected != nil {
		t.Fatalf("expected no selection, got %+v", got.Selected)
	}
}

func TestAvailableNeverNegative(t *testing.T) {
	c := NodeCandidate{MaxServers: 5, UsedServers: 9}
	if c.Available() != 0 {
		t.Fatalf("expected 0 available when over capacity, got %d", c.Available())
	}
}
