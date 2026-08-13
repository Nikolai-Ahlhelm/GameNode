package audit

import (
	"context"
	"encoding/json"
	"gamenode"
	"gamenode/internal/database"
	"strings"
	"testing"
	"time"
)

func service(t *testing.T) *Service {
	db, e := database.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	if e = database.Migrate(db, gamenode.MigrationFiles); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}
func TestRecordListFiltersAndMetadata(t *testing.T) {
	s := service(t)
	ctx := context.Background()
	actor := "u"
	server := "s"
	rid := "r"
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := Event{Timestamp: stamp, ActorUserID: &actor, ActorUsername: "user", Action: ServerStart, ResourceType: Server, ResourceID: &rid, ServerID: &server, Result: Success, Metadata: []byte(`{"port":25565}`)}
	if err := s.Record(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, Event{Timestamp: stamp, Action: PortCreate, ResourceType: Port, Result: Failure, ErrorCode: "conflict", ErrorSummary: "taken"}); err != nil {
		t.Fatal(err)
	}
	x, err := s.List(ctx, Filter{ActorUserID: &actor, Limit: 1})
	if err != nil || len(x) != 1 || string(x[0].Metadata) != "{\"port\":25565}" {
		t.Fatalf("roundtrip %#v %v", x, err)
	}
	x, err = s.List(ctx, Filter{Result: Failure, Action: PortCreate})
	if err != nil || len(x) != 1 || x[0].ErrorCode != "conflict" {
		t.Fatal(x, err)
	}
}
func TestMetadataLimitsAndInvalidEvents(t *testing.T) {
	s := service(t)
	ctx := context.Background()
	for _, e := range []Event{{Action: "", ResourceType: Server, Result: Success}, {Action: ServerStart, ResourceType: Server, Result: "bad"}, {Action: ServerStart, ResourceType: Server, Result: Success, Metadata: []byte("{")}, {Action: ServerStart, ResourceType: Server, Result: Success, Metadata: []byte(strings.Repeat("x", MaxMetadataBytes+1))}} {
		if s.Record(ctx, e) == nil {
			t.Fatal("invalid event accepted")
		}
	}
	if err := s.Record(ctx, Event{Action: ServerStart, ResourceType: Server, Result: Success, Metadata: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestEventJSONContractUsesFrontendFieldNames(t *testing.T) {
	actor, resource, server := "user-1", "resource-1", "server-1"
	event := Event{ID: "event-1", Timestamp: time.Date(2026, 8, 13, 10, 11, 12, 123000000, time.UTC), ActorUserID: &actor, ActorUsername: "admin", Action: ServerStart, ResourceType: Server, ResourceID: &resource, ResourceName: "Game server", ServerID: &server, Result: Success, RemoteIP: "127.0.0.1", Metadata: json.RawMessage(`{"source":"manual"}`)}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "timestamp", "actor_user_id", "actor_username", "action", "resource_type", "resource_id", "resource_name", "server_id", "result", "remote_ip", "metadata"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing JSON field %q in %s", key, data)
		}
	}
	if _, leaked := decoded["Timestamp"]; leaked {
		t.Fatalf("Go field name leaked into API response: %s", data)
	}
}

func TestListQuerySearchesUsefulFieldsAndEscapesWildcards(t *testing.T) {
	s := service(t)
	ctx := context.Background()
	actor := "user-1"
	if err := s.Record(ctx, Event{ActorUserID: &actor, ActorUsername: "alice", Action: ServerStart, ResourceType: Server, ResourceName: "Survival world", Result: Success}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, Event{ActorUsername: "bob", Action: PortCreate, ResourceType: Port, ResourceName: "query_%_literal", Result: Failure, ErrorCode: "port_conflict", ErrorSummary: "Port is occupied"}); err != nil {
		t.Fatal(err)
	}
	for query, action := range map[string]string{"alice": ServerStart, "survival": ServerStart, "occupied": PortCreate, "_%_": PortCreate} {
		events, err := s.List(ctx, Filter{Query: query})
		if err != nil || len(events) != 1 || events[0].Action != action {
			t.Fatalf("query %q returned %#v, err=%v", query, events, err)
		}
	}
}
