package ports

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Port struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	BindAddress string `json:"bind_address"`
	Port        int    `json:"port"`
	Status      string `json:"status"`
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
	rows, e := s.db.QueryContext(c, "SELECT id,name,protocol,bind_address,port FROM server_ports WHERE server_id=? ORDER BY protocol,port,name", server)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Port
	for rows.Next() {
		var p Port
		if e = rows.Scan(&p.ID, &p.Name, &p.Protocol, &p.BindAddress, &p.Port); e != nil {
			return nil, e
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
	_, e := s.db.ExecContext(c, "INSERT INTO server_ports(id,server_id,name,protocol,bind_address,port,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", p.ID, server, p.Name, p.Protocol, p.BindAddress, p.Port, n, n)
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
	r, e := s.db.ExecContext(c, "UPDATE server_ports SET name=?,protocol=?,bind_address=?,port=?,updated_at=? WHERE id=? AND server_id=?", p.Name, p.Protocol, p.BindAddress, p.Port, time.Now().UTC().Format(time.RFC3339Nano), id, server)
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
func (s *Service) check(c context.Context, server string, p Port, exclude string) error {
	rows, e := s.db.QueryContext(c, "SELECT id,name,protocol,bind_address,port FROM server_ports WHERE id<>?", exclude)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var q Port
		if e = rows.Scan(&q.ID, &q.Name, &q.Protocol, &q.BindAddress, &q.Port); e != nil {
			return e
		}
		if Conflict(p, q) {
			return errors.New("port conflicts with another GameNode server")
		}
	}
	if s.status(p) == "in_use" {
		return errors.New("port is already in use on this host")
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
func id() string           { b := make([]byte, 16); rand.Read(b); return hex.EncodeToString(b) }
