package ports

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Port struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	BindAddress   string `json:"bind_address"`
	Port          int    `json:"port"`
	ContainerPort int    `json:"container_port,omitempty"`
	Status        string `json:"status"`
}
type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db} }
func Validate(p *Port) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Protocol = strings.ToLower(strings.TrimSpace(p.Protocol))
	p.BindAddress = strings.Trim(strings.TrimSpace(p.BindAddress), "[]")
	if p.Protocol != "tcp" && p.Protocol != "udp" {
		return errors.New("protocol must be tcp or udp")
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if p.ContainerPort < 0 || p.ContainerPort > 65535 {
		return errors.New("container port must be between 1 and 65535")
	}
	if p.BindAddress != "" && p.BindAddress != "0.0.0.0" && p.BindAddress != "::" {
		if _, e := netip.ParseAddr(p.BindAddress); e != nil {
			return errors.New("bind address must be a local IP address or wildcard")
		}
	}
	return nil
}
func Conflict(a, b Port) bool {
	if a.Protocol != b.Protocol || a.Port != b.Port {
		return false
	}
	if a.BindAddress == b.BindAddress {
		return true
	}
	return a.BindAddress == "" || b.BindAddress == "" || a.BindAddress == "0.0.0.0" || b.BindAddress == "0.0.0.0" || a.BindAddress == "::" || b.BindAddress == "::"
}
func (s *Service) List(c context.Context, server string) ([]Port, error) {
	rows, e := s.db.QueryContext(c, "SELECT id,name,protocol,bind_address,port,container_port FROM server_ports WHERE server_id=? ORDER BY protocol,port,name", server)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Port
	for rows.Next() {
		var p Port
		var target sql.NullInt64
		if e = rows.Scan(&p.ID, &p.Name, &p.Protocol, &p.BindAddress, &p.Port, &target); e != nil {
			return nil, e
		}
		if target.Valid {
			p.ContainerPort = int(target.Int64)
		}
		p.Status = s.status(p)
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) Add(c context.Context, server string, p Port) (Port, error) {
	if e := Validate(&p); e != nil {
		return p, e
	}
	if e := s.check(c, server, p, ""); e != nil {
		return p, e
	}
	p.ID = id()
	n := time.Now().UTC().Format(time.RFC3339Nano)
	_, e := s.db.ExecContext(c, "INSERT INTO server_ports(id,server_id,name,protocol,bind_address,port,container_port,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)", p.ID, server, p.Name, p.Protocol, p.BindAddress, p.Port, nullablePort(p.ContainerPort), n, n)
	p.Status = s.status(p)
	return p, e
}
func (s *Service) Update(c context.Context, server, id string, p Port) (Port, error) {
	if e := Validate(&p); e != nil {
		return p, e
	}
	if e := s.check(c, server, p, id); e != nil {
		return p, e
	}
	r, e := s.db.ExecContext(c, "UPDATE server_ports SET name=?,protocol=?,bind_address=?,port=?,container_port=?,updated_at=? WHERE id=? AND server_id=?", p.Name, p.Protocol, p.BindAddress, p.Port, nullablePort(p.ContainerPort), time.Now().UTC().Format(time.RFC3339Nano), id, server)
	if e != nil {
		return p, e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return p, sql.ErrNoRows
	}
	p.ID = id
	p.Status = s.status(p)
	return p, nil
}
func (s *Service) Delete(c context.Context, server, id string) error {
	r, e := s.db.ExecContext(c, "DELETE FROM server_ports WHERE id=? AND server_id=?", id, server)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Service) Check(c context.Context, server string) error {
	ps, e := s.List(c, server)
	if e != nil {
		return e
	}
	for _, p := range ps {
		if e = s.check(c, server, p, p.ID); e != nil {
			return e
		}
	}
	return nil
}

// CheckCandidates validates a set of not-yet-persisted ports against every
// currently registered GameNode port and a best-effort OS availability
// probe, without writing anything. It is the same authoritative collision
// logic Add/Update/Check use, exposed for callers that need to preflight
// ports before they exist in server_ports - such as provisioning, which
// resolves a template's ports before SteamCMD installation starts so a
// known conflict fails fast instead of after a full game download. This is
// a best-effort, point-in-time check: it does not reserve the ports, so a
// port can still become occupied afterward (see Check, which the final
// server registration also runs).
func (s *Service) CheckCandidates(c context.Context, candidates []Port) error {
	for i := range candidates {
		p := candidates[i]
		if e := Validate(&p); e != nil {
			return e
		}
		for j := range candidates {
			if i != j && Conflict(p, candidates[j]) {
				return fmt.Errorf("%d/%s port conflicts with another requested port", p.Port, strings.ToUpper(p.Protocol))
			}
		}
		if e := s.check(c, "", p, ""); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) check(c context.Context, server string, p Port, exclude string) error {
	rows, e := s.db.QueryContext(c, "SELECT id,name,protocol,bind_address,port,container_port FROM server_ports WHERE id<>?", exclude)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var q Port
		var target sql.NullInt64
		if e = rows.Scan(&q.ID, &q.Name, &q.Protocol, &q.BindAddress, &q.Port, &target); e != nil {
			return e
		}
		if target.Valid {
			q.ContainerPort = int(target.Int64)
		}
		if Conflict(p, q) {
			return fmt.Errorf("%d/%s port conflicts with another GameNode server", p.Port, strings.ToUpper(p.Protocol))
		}
	}
	if s.status(p) == "in_use" {
		return fmt.Errorf("%d/%s port is already in use on this host", p.Port, strings.ToUpper(p.Protocol))
	}
	return nil
}
func (s *Service) status(p Port) string {
	host := p.BindAddress
	if host == "" {
		host = "0.0.0.0"
	}
	addr := net.JoinHostPort(host, fmtPort(p.Port))
	if p.Protocol == "tcp" {
		l, e := net.Listen("tcp", addr)
		if e != nil {
			return "in_use"
		}
		l.Close()
		return "available"
	}
	l, e := net.ListenPacket("udp", addr)
	if e != nil {
		return "in_use"
	}
	l.Close()
	return "available"
}
func fmtPort(n int) string { return strconv.Itoa(n) }
func nullablePort(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
func id() string { b := make([]byte, 16); rand.Read(b); return hex.EncodeToString(b) }
