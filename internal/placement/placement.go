// Package placement implements the v0.6 Cluster Scheduling foundation: a
// deterministic, read-only PLACEMENT DECISION (which node is suitable for a
// new server) built from data other packages already expose. It never
// mutates a server, never contacts a remote node, and is completely
// independent of internal/scheduler (the v0.4 typed LOCAL restart
// scheduler, which only calls servers.Service.Restart on a timer and has
// nothing to do with node selection).
//
// See docs/adr/0009-cluster-scheduling-decision-vs-execution.md for the
// Decision vs. Execution boundary this package deliberately stops at: it
// never creates a server anywhere, on the local node or a remote one. A
// caller that wants to act on a "local_only" decision does so through the
// ordinary servers.Service/provisioning API, unchanged by this package.
package placement

import (
	"sort"
)

// NodeKind distinguishes the node this installation itself is running on
// from a node enrolled in the Remote Node registry (internal/nodes).
type NodeKind string

const (
	NodeLocal  NodeKind = "local"
	NodeRemote NodeKind = "remote"
)

// Execution classifies whether a selected node's server could actually be
// created as a direct consequence of this decision. It is presentation/
// contract metadata only - this package never performs the creation itself
// even for "local_only" (see package doc and the ADR).
type Execution string

const (
	// ExecutionLocalOnly means the selected node is this installation. The
	// existing servers.Service/provisioning create path can be used to act
	// on the decision; nothing about that path is changed or bypassed here.
	ExecutionLocalOnly Execution = "local_only"
	// ExecutionRemoteExecutable means the selected node is a Remote Node
	// that now HAS a working remote server creation path (v0.5B Remote
	// Server Management, see docs/adr/0010-remote-server-lifecycle-forwarding.md):
	// a caller COULD act on this decision through
	// POST /api/v1/remote-nodes/{id}/servers. This package still never does
	// so itself - it is a status/capability label only, not an execution
	// trigger. Before v0.5B this value was named/spelled "requires_v0.5b";
	// it is renamed now that the prerequisite genuinely exists (see
	// PROJECT_PLAN.md's v0.5B section).
	ExecutionRemoteExecutable Execution = "remote_executable"
)

// Reason enumerates why a candidate node was excluded, or why no node could
// be selected at all. Values are stable strings so API/audit consumers can
// match on them without parsing free text.
type Reason string

const (
	ReasonDisabled          Reason = "disabled"
	ReasonUnhealthy         Reason = "unhealthy"
	ReasonMissingCapability Reason = "missing_capability"
	ReasonCapacityExhausted Reason = "capacity_exhausted"
	ReasonNoEligibleNode    Reason = "no_eligible_node"
	ReasonSelected          Reason = ""
)

// NodeCandidate is the bounded, already-fetched-by-the-caller view of one
// node this engine may consider. It carries no live service handles -
// building this struct (from internal/servers, internal/nodes) is the
// caller's job, which is what keeps Decide pure and deterministically
// testable with fixed inputs and no network/database dependency.
type NodeCandidate struct {
	NodeID       string
	DisplayName  string
	Kind         NodeKind
	Enabled      bool
	Healthy      bool
	Capabilities []string

	// CapacityKnown is false for every Remote Node candidate today: v0.5A's
	// read-only Node API (GetNodeInfo/GetHealth/GetCapabilities) does not
	// expose server counts or resource usage, and this milestone
	// deliberately does not add a remote server-listing call (that would be
	// v0.5B/v0.5C scope - see docs/adr/0009-cluster-scheduling-decision-vs-execution.md).
	// A candidate with CapacityKnown=false is still eligible, but is always
	// ranked below every candidate with verified spare capacity.
	CapacityKnown bool
	UsedServers   int
	MaxServers    int
}

// Available reports remaining capacity for a capacity-known candidate. It is
// meaningless (and never consulted) when CapacityKnown is false.
func (c NodeCandidate) Available() int {
	if c.MaxServers <= c.UsedServers {
		return 0
	}
	return c.MaxServers - c.UsedServers
}

// Request is the tenant-scoped, capability-scoped placement request. The
// caller (internal/api) is responsible for tenant RBAC and for never
// including another tenant's candidates or capacity numbers - this engine
// only ever sees the one candidate list it is given.
type Request struct {
	TenantID             string
	RequiredCapabilities []string
	Candidates           []NodeCandidate
}

// CandidateResult reports the outcome for exactly one evaluated candidate,
// selected or not, so a caller/API/UI can show why a node was skipped.
type CandidateResult struct {
	NodeID      string
	DisplayName string
	Kind        NodeKind
	Selected    bool
	Reason      Reason
}

// Decision is the deterministic result of evaluating a Request.
type Decision struct {
	TenantID   string
	Rejected   bool
	Reason     Reason
	Selected   *NodeCandidate
	Execution  Execution
	Candidates []CandidateResult
}

// Decide computes a deterministic best-fit placement decision. It performs
// no I/O and has no dependency on time, randomness, or ordering the caller
// did not already provide, so identical input always yields an identical
// Decision - this is what makes the unit tests in placement_test.go exact
// input/output comparisons rather than approximations.
//
// Algorithm: exclude disabled, unhealthy, and capability-mismatched
// candidates; exclude capacity-known candidates with no spare capacity;
// among the remainder, prefer capacity-known candidates (most spare
// capacity first) over capacity-unknown ones, and break every remaining tie
// by NodeID ascending so the result never depends on input/map order.
func Decide(req Request) Decision {
	decision := Decision{TenantID: req.TenantID}
	eligible := make([]NodeCandidate, 0, len(req.Candidates))
	results := make([]CandidateResult, 0, len(req.Candidates))

	for _, c := range req.Candidates {
		reason := evaluate(c, req.RequiredCapabilities)
		if reason == ReasonSelected {
			eligible = append(eligible, c)
		}
		results = append(results, CandidateResult{NodeID: c.NodeID, DisplayName: c.DisplayName, Kind: c.Kind, Reason: reason})
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if a.CapacityKnown != b.CapacityKnown {
			return a.CapacityKnown // known-capacity candidates sort first
		}
		if a.CapacityKnown && b.CapacityKnown && a.Available() != b.Available() {
			return a.Available() > b.Available() // most spare capacity first
		}
		return a.NodeID < b.NodeID // deterministic tie-break
	})

	if len(eligible) == 0 {
		decision.Rejected = true
		decision.Reason = ReasonNoEligibleNode
		decision.Candidates = results
		return decision
	}

	selected := eligible[0]
	decision.Selected = &selected
	if selected.Kind == NodeLocal {
		decision.Execution = ExecutionLocalOnly
	} else {
		decision.Execution = ExecutionRemoteExecutable
	}
	for i := range results {
		if results[i].NodeID == selected.NodeID && results[i].Kind == selected.Kind {
			results[i].Selected = true
		}
	}
	decision.Candidates = results
	return decision
}

func evaluate(c NodeCandidate, required []string) Reason {
	if !c.Enabled {
		return ReasonDisabled
	}
	if !c.Healthy {
		return ReasonUnhealthy
	}
	if !hasAllCapabilities(c.Capabilities, required) {
		return ReasonMissingCapability
	}
	if c.CapacityKnown && c.Available() <= 0 {
		return ReasonCapacityExhausted
	}
	return ReasonSelected
}

func hasAllCapabilities(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}
