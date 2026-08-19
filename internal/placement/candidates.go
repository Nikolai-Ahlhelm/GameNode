package placement

import (
	"gamenode/internal/nodeidentity"
	"gamenode/internal/nodes"
	"gamenode/internal/servers"
)

// DefaultMaxServersPerNode is the fixed, non-configurable capacity ceiling
// used for the local node's capacity model in this milestone. There is no
// existing per-node capacity setting anywhere in the product
// (internal/settings has no such field), and inventing a configuration
// surface for it is out of scope for a foundation milestone whose primary
// job is the placement DECISION, not capacity administration - see
// docs/adr/0009-cluster-scheduling-decision-vs-execution.md. A future
// milestone can make this operator-configurable without changing the
// Decide algorithm above.
const DefaultMaxServersPerNode = 50

// RuntimeCapability maps a servers.Server.RuntimeType value to the
// nodeidentity.Capability string a candidate node must advertise to be
// eligible to host it. Only the two runtime types the product actually has
// are mapped; an unrecognized value maps to itself so an unexpected future
// runtime type still produces a legible (if unmatched) capability rather
// than a panic.
func RuntimeCapability(runtimeType string) string {
	switch runtimeType {
	case "container":
		return string(nodeidentity.CapabilityContainerRuntime)
	default:
		return string(nodeidentity.CapabilityNativeRuntime)
	}
}

// LocalCandidate builds this installation's own placement candidate. Usage
// is a simple count of every server this node's servers.Service already
// tracks (not filtered to the requesting tenant): node capacity is a
// node-wide resource shared across every tenant hosted on it, so the
// candidate reflects total load, while tenant isolation is enforced
// separately by the caller (RBAC scope on the request itself, never by
// hiding or altering capacity numbers). Capabilities come from
// nodeidentity.Capabilities(), the same fixed, reviewed list this node
// advertises to a Remote Node controller.
func LocalCandidate(info nodeidentity.Info, allServers []servers.Record) NodeCandidate {
	name := info.DisplayName
	if name == "" {
		name = "This node (" + info.NodeID + ")"
	}
	caps := make([]string, 0, len(info.Capabilities))
	for _, c := range info.Capabilities {
		caps = append(caps, string(c))
	}
	return NodeCandidate{
		NodeID:        "local",
		DisplayName:   name,
		Kind:          NodeLocal,
		Enabled:       true,
		Healthy:       true,
		Capabilities:  caps,
		CapacityKnown: true,
		UsedServers:   len(allServers),
		MaxServers:    DefaultMaxServersPerNode,
	}
}

// RemoteCandidates builds one candidate per enrolled Remote Node registry
// entry. Health translates only nodes.HealthReachable to eligible/healthy;
// every other state (unreachable, authentication_failed,
// protocol_incompatible, degraded, unknown) is treated as unhealthy,
// consistent with AGENTS.md's rule that remote connection state is
// presentation-only and must never be upgraded into an assumption of
// fitness. Capacity is always unknown - see NodeCandidate.CapacityKnown's
// doc comment for why this milestone does not add a remote capacity call.
func RemoteCandidates(remote []nodes.RemoteNode) []NodeCandidate {
	out := make([]NodeCandidate, 0, len(remote))
	for _, n := range remote {
		out = append(out, NodeCandidate{
			NodeID:        n.ID,
			DisplayName:   n.DisplayName,
			Kind:          NodeRemote,
			Enabled:       n.Enabled,
			Healthy:       n.Enabled && n.LastHealth == nodes.HealthReachable,
			Capabilities:  append([]string(nil), n.Capabilities...),
			CapacityKnown: false,
		})
	}
	return out
}
