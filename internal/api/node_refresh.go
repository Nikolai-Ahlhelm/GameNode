package api

import (
	"context"
	"errors"
	"time"

	"gamenode/internal/nodeidentity"
	"gamenode/internal/nodes"
	"gamenode/internal/remote"
)

// localProtocolVersion is this build's own Remote Node protocol version,
// used only to classify a remote node's advertised version into a
// presentation-level compatibility bucket (see AGENTS.md item 32). It is
// never compared to product/release version strings.
const localProtocolVersion = nodeidentity.ProtocolVersion

// refreshInterval bounds how often the background loop below re-checks each
// enrolled, enabled remote node. This is deliberately conservative
// reachability polling, not a distributed heartbeat/consensus protocol (see
// AGENTS.md item 20).
const refreshInterval = 30 * time.Second

// refreshTimeout bounds a single remote node's refresh call so one
// unreachable node can never stall the loop or the others in it.
const refreshTimeout = 5 * time.Second

// refreshOne performs one bounded status check against a single remote node
// and persists the result. It never treats the outcome as authoritative
// over the remote node's own server/runtime state (see AGENTS.md item 21).
func (s *Server) refreshOne(ctx context.Context, n nodes.RemoteNode) {
	reqCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	info, err := s.remoteClient.GetNodeInfo(reqCtx, n.Endpoint, n.Credential)
	update := nodes.StatusUpdate{}
	if err != nil {
		update.Health = classifyHealth(err)
		update.ErrorCode = healthErrorCode(err)
		// Preserve last-known-good metadata on a failed refresh rather than
		// zeroing it out.
		update.ProtocolVersion = n.ProtocolVersion
		update.GameNodeVersion = n.GameNodeVersion
		update.OS = n.OS
		update.Arch = n.Arch
		update.Capabilities = n.Capabilities
	} else {
		update.Health = nodes.HealthReachable
		update.ProtocolVersion = info.ProtocolVersion
		update.GameNodeVersion = info.GameNodeVersion
		update.OS = info.OS
		update.Arch = info.Arch
		update.Capabilities = info.Capabilities
		if info.ProtocolVersion > localProtocolVersion {
			update.Health = nodes.HealthDegraded
			update.ErrorCode = "node_protocol_newer"
		}
	}
	// Best-effort: a persistence failure here must not crash the refresh
	// loop or propagate to the caller of a manual refresh.
	_ = s.nodes.ApplyStatus(ctx, n.ID, update)
}

func classifyHealth(err error) nodes.Health {
	var remoteErr *remote.Error
	if errors.As(err, &remoteErr) {
		switch remoteErr.Kind {
		case remote.KindAuthenticationFailed:
			return nodes.HealthAuthenticationFailed
		case remote.KindProtocolIncompatible:
			return nodes.HealthProtocolIncompatible
		case remote.KindUnreachable:
			return nodes.HealthUnreachable
		default:
			return nodes.HealthDegraded
		}
	}
	return nodes.HealthUnreachable
}

func healthErrorCode(err error) string {
	var remoteErr *remote.Error
	if errors.As(err, &remoteErr) {
		return string(remoteErr.Kind)
	}
	return "node_unreachable"
}

// StartHeartbeat launches the bounded, periodic remote-node status
// refresher. It returns a stop function that cancels the loop and waits for
// the in-flight refresh (if any) to finish, so shutdown never leaks a
// goroutine (see AGENTS.md item 20). One unreachable node's bounded timeout
// never blocks the others - each refresh in a tick runs concurrently and
// the tick itself waits for all of them before sleeping again.
func (s *Server) StartHeartbeat() (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshAllEnabled(ctx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *Server) refreshAllEnabled(ctx context.Context) {
	list, err := s.nodes.List(ctx)
	if err != nil {
		return
	}
	done := make(chan struct{})
	remaining := 0
	for _, n := range list {
		if !n.Enabled {
			continue
		}
		remaining++
		go func(n nodes.RemoteNode) {
			defer func() { done <- struct{}{} }()
			s.refreshOne(ctx, n)
		}(n)
	}
	for i := 0; i < remaining; i++ {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}
