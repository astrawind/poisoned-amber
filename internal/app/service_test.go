package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	db "poisoned-amber/internal/adapters/sqlite"
	"poisoned-amber/internal/domain"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fake struct{ messages map[string][]Message }

func (f *fake) Send(_ context.Context, chat string, m Message, _ int64) (int64, error) {
	f.messages[chat] = append(f.messages[chat], m)
	return int64(len(f.messages[chat])), nil
}
func TestOfflineLobbyCombatRecoveryAndDuplicate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "game.db")
	s, e := db.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open("../../content/test.json")
	if e != nil {
		t.Fatal(e)
	}
	c, e := domain.ReadContent(f)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Import(c); e != nil {
		t.Fatal(e)
	}
	svc := New(s, c.Version)
	now := int64(1000)
	svc.Now = func() time.Time { return time.Unix(now, 0) }
	counter := 0
	send := func(user, text string) {
		t.Helper()
		counter++
		if e := svc.Handle(ctx, Input{ID: fmt.Sprint(counter), User: user, Name: user, Text: text}); e != nil {
			t.Fatal(e)
		}
	}
	send("1", "/adult")
	send("2", "/adult")
	send("1", "/create password")
	var rid, code string
	if e = s.DB.QueryRow("SELECT id,code FROM rooms").Scan(&rid, &code); e != nil {
		t.Fatal(e)
	}
	send("2", "/join "+code+" password")
	for _, u := range []string{"1", "2"} {
		send(u, "/class warrior")
		send(u, "/background miner")
		send(u, "/ready")
	}
	send("1", "/begin")
	read := func() *domain.Room {
		t.Helper()
		var r *domain.Room
		e := s.Tx(ctx, func(tx *sql.Tx) error { var e error; r, e = db.Room(tx, rid); return e })
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := read()
	if r.Phase != "combat" {
		t.Fatal(r.Phase)
	}
	in := Input{ID: "duplicate", User: "1", Text: "/act warrior_attack_common 0"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := svc.Handle(ctx, in); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	r = read()
	if !r.Players[0].Acted {
		t.Fatal("missing effect")
	}
	snapshot, _ := json.Marshal(r)
	s.Close()
	s, e = db.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	svc = New(s, c.Version)
	svc.Now = func() time.Time { return time.Unix(now, 0) }
	if e = svc.Handle(ctx, in); e != nil {
		t.Fatal(e)
	}
	after, _ := json.Marshal(read())
	if string(snapshot) != string(after) {
		t.Fatal("duplicate after restart")
	}
	now = r.Deadline
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := svc.Tick(ctx); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	r = read()
	if r.Round != 2 {
		t.Fatal("timeout applied repeatedly", r.Round)
	}
	adapter := &fake{messages: map[string][]Message{}}
	for {
		worked, e := svc.Deliver(ctx, adapter, func(error) (time.Duration, bool) { return time.Second, false })
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			break
		}
	}
	if len(adapter.messages["1"]) == 0 || len(adapter.messages["2"]) == 0 {
		t.Fatal("delivery")
	}
	// Version IDs are immutable; active rooms keep their original content.
	c.Version = "test-2"
	c.Classes[0].Base.HP++
	if e = s.Import(c); e != nil {
		t.Fatal(e)
	}
	if read().ContentVersion != "test-1" {
		t.Fatal("content changed")
	}
}

func TestSecretsRewardsPauseAndDeadlineRace(t *testing.T) {
	ctx := context.Background()
	s, e := db.Open(filepath.Join(t.TempDir(), "s.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	f, _ := os.Open("../../content/test.json")
	c, e := domain.ReadContent(f)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Import(c); e != nil {
		t.Fatal(e)
	}
	svc := New(s, c.Version)
	now := int64(100)
	svc.Now = func() time.Time { return time.Unix(now, 0) }
	r := &domain.Room{ID: "r", Code: "ROOM", ContentVersion: c.Version, Phase: "challenge", PhaseID: 3, Deadline: 200, LastAction: 100, Leader: "1", Encounter: 2, Players: []domain.Fighter{{ID: "1", Name: "one", Class: "warrior", HP: 10, Slots: 2, Memory: []string{"warrior_attack_common"}}, {ID: "2", Name: "two", Class: "ranger", HP: 0, Slots: 2, Memory: []string{"ranger_attack_common"}}}, Challenge: domain.Challenge{Kind: "crocodile", Secret: "w0", Words: []string{"молот"}, Performer: "1"}}
	if e = s.Tx(ctx, func(tx *sql.Tx) error {
		for _, u := range []string{"1", "2"} {
			if _, e := tx.Exec("INSERT INTO users(id,adult,room) VALUES(?,1,'r')", u); e != nil {
				return e
			}
		}
		if e := db.Save(tx, r, c); e != nil {
			return e
		}
		return svc.broadcast(tx, r, c)
	}); e != nil {
		t.Fatal(e)
	}
	rows, e := s.DB.Query("SELECT chat,body FROM outbox")
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var chat string
		var raw []byte
		rows.Scan(&chat, &raw)
		var m Message
		json.Unmarshal(raw, &m)
		if chat == "2" && strings.Contains(m.Text, "молот") {
			t.Fatal("secret leaked")
		}
	}
	rows.Close()
	send := func(id, text string) {
		t.Helper()
		if e := svc.Handle(ctx, Input{ID: id, User: "1", Text: text}); e != nil {
			t.Fatal(e)
		}
	}
	send("solve", "/answer solved")
	var saved *domain.Room
	read := func() {
		t.Helper()
		if e = s.Tx(ctx, func(tx *sql.Tx) error { var e error; saved, e = db.Room(tx, "r"); return e }); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if saved.Players[1].Slots != 3 || len(saved.Players[1].Offer) != 0 {
		t.Fatal("fallen reward")
	}
	offer := append([]string{}, saved.Players[0].Offer...)
	send("pause", "/pause")
	now += 30
	send("resume", "/resume")
	read()
	if !slices.Equal(offer, saved.Players[0].Offer) {
		t.Fatal("pause rerolled rewards")
	}
	now = saved.Deadline
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if e := svc.Tick(ctx); e != nil {
			t.Error(e)
		}
	}()
	go func() {
		defer wg.Done()
		if e := svc.Handle(ctx, Input{ID: "late", User: "1", Text: "/confirm"}); e != nil {
			t.Error(e)
		}
	}()
	wg.Wait()
	read()
	if saved.Phase != "transition" || saved.Players[0].Memory[0] != "warrior_attack_common" {
		t.Fatal("deadline race")
	}
}

type selectiveSender struct{ seen []string }

func (f *selectiveSender) Send(_ context.Context, chat string, _ Message, _ int64) (int64, error) {
	f.seen = append(f.seen, chat)
	if chat == "blocked" {
		return 0, fmt.Errorf("unavailable")
	}
	return 1, nil
}
func TestOutboxRecipientIsolationAndOrdering(t *testing.T) {
	s, e := db.Open(filepath.Join(t.TempDir(), "q.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	svc := New(s, "")
	svc.Now = func() time.Time { return time.Unix(100, 0) }
	ctx := context.Background()
	if e = s.Tx(ctx, func(tx *sql.Tx) error {
		for _, chat := range []string{"blocked", "blocked", "healthy", "healthy"} {
			if e := enqueue(tx, chat, Message{Text: "test"}); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	sender := &selectiveSender{}
	for i := 0; i < 4; i++ {
		if _, e = svc.Deliver(ctx, sender, func(error) (time.Duration, bool) { return 30 * time.Second, false }); e != nil {
			t.Fatal(e)
		}
	}
	if !slices.Equal(sender.seen, []string{"blocked", "healthy", "healthy"}) {
		t.Fatal(sender.seen)
	}
}
