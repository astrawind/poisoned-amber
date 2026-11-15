package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	db "poisoned-amber/internal/adapters/sqlite"
	"poisoned-amber/internal/adapters/telegram"
	"poisoned-amber/internal/app"
	"poisoned-amber/internal/domain"
	"strings"
	"testing"
)

func TestDiagnosticsDescribeStateWithoutSecrets(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "debug.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	err = store.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO users(id,room) VALUES('private-user','room')"); err != nil {
			return err
		}
		return db.Save(tx, &domain.Room{ID: "room", Code: "SECRET-CODE", Password: "SECRET-HASH", Phase: "vote", PhaseID: 19, Encounter: 8}, &domain.Content{})
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	d := diagnostics{logger: log.New(&output, "", 0), debug: true}
	d.handled(ctx, store.DB, app.Input{User: "private-user", Text: "/join SECRET-CODE SECRET-PASSWORD", Callback: "SECRET-CALLBACK", Offset: 42})
	d.failure("send", fmt.Errorf("SECRET-TOKEN: %w", &telegram.APIError{Code: 429, Retry: 7}))
	d.failure("poll", fmt.Errorf("SECRET-URL"))
	got := output.String()
	for _, want := range []string{"offset=42 kind=callback phase=vote phase_id=19 encounter=9", "telegram_code=429 retry_after=7", "category=transport_or_internal"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "SECRET") || strings.Contains(got, "private-user") {
		t.Fatal("diagnostics exposed private data")
	}
}
