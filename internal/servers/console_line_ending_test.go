package servers

import (
	"context"
	"strings"
	"testing"
)

func TestConsoleInputConvertsLineEndingsOnlyForCRLFServers(t *testing.T) {
	lf := Server{}
	crlf := Server{ConsoleLineEnding: ConsoleLineEndingCRLF}
	cases := []struct{ in, wantLF, wantCRLF string }{
		{"/stop\n", "/stop\n", "/stop\r\n"},
		{"/stop\r\n", "/stop\r\n", "/stop\r\n"},
		{"a\nb\n", "a\nb\n", "a\r\nb\r\n"},
		{"no newline", "no newline", "no newline"},
	}
	for _, test := range cases {
		if got := lf.ConsoleInput(test.in); got != test.wantLF {
			t.Errorf("lf %q = %q want %q", test.in, got, test.wantLF)
		}
		if got := crlf.ConsoleInput(test.in); got != test.wantCRLF {
			t.Errorf("crlf %q = %q want %q", test.in, got, test.wantCRLF)
		}
	}
}

func TestConsoleLineEndingValidatesPersistsAndSurvivesEdits(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	plain := testServer(t)
	plain.Name = "plain"
	record, err := service.Create(context.Background(), plain)
	if err != nil || record.Server.ConsoleLineEnding != ConsoleLineEndingLF {
		t.Fatalf("an unset line ending must default to lf: %q %v", record.Server.ConsoleLineEnding, err)
	}
	bad := testServer(t)
	bad.Name = "bad"
	bad.ConsoleLineEnding = "cr"
	if _, err = service.Create(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "line ending") {
		t.Fatalf("an unsupported line ending must be rejected: %v", err)
	}
	special := testServer(t)
	special.Name = "special"
	special.ConsoleLineEnding = ConsoleLineEndingCRLF
	created, err := service.Create(context.Background(), special)
	if err != nil || created.Server.ConsoleLineEnding != ConsoleLineEndingCRLF {
		t.Fatalf("crlf must persist: %q %v", created.Server.ConsoleLineEnding, err)
	}
	edit := created.Server
	edit.Description = "edited"
	edit.ConsoleLineEnding = "" // an edit request that does not carry the field
	updated, err := service.Update(context.Background(), created.Server.ID, edit)
	if err != nil || updated.Server.ConsoleLineEnding != ConsoleLineEndingCRLF || updated.Server.Description != "edited" {
		t.Fatalf("an edit must not reset the stored line ending: %q %v", updated.Server.ConsoleLineEnding, err)
	}
	reloaded, err := service.Get(context.Background(), created.Server.ID)
	if err != nil || reloaded.Server.ConsoleLineEnding != ConsoleLineEndingCRLF {
		t.Fatalf("reloaded = %q %v", reloaded.Server.ConsoleLineEnding, err)
	}
}
