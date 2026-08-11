package audit

import (
	"context"
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
