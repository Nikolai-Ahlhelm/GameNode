package dashboard

import "gamenode/internal/monitoring"

type Server struct {
	State      string
	Monitoring monitoring.Snapshot
}
type Port struct{ Protocol string }
type Summary struct {
	Servers    map[string]int `json:"servers"`
	Monitoring map[string]int `json:"monitoring"`
	Ports      map[string]int `json:"ports"`
}

func Aggregate(servers []Server, ports []Port) Summary {
	s := Summary{Servers: map[string]int{"total": len(servers)}, Monitoring: map[string]int{}, Ports: map[string]int{"total": len(ports)}}
	for _, x := range servers {
		s.Servers[x.State]++
		s.Monitoring["visible_servers"]++
		s.Monitoring["total_crashes"] += x.Monitoring.CrashCount
		s.Monitoring["total_restarts"] += x.Monitoring.RestartCount
		if x.Monitoring.Health == "degraded" || x.Monitoring.Health == "crashed" {
			s.Monitoring["degraded"]++
		}
		if x.Monitoring.AutoRestartEnabled {
			s.Monitoring["auto_restart_enabled"]++
		}
		if x.Monitoring.RestartLimitReached {
			s.Monitoring["restart_limit_reached"]++
		}
		if x.Monitoring.PendingAutoRestart {
			s.Monitoring["pending_auto_restart"]++
		}
	}
	for _, p := range ports {
		s.Ports[p.Protocol]++
	}
	return s
}
