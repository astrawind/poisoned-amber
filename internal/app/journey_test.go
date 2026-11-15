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
	"strings"
	"sync"
	"testing"
	"time"
)

type journeyFixture struct {
	t       *testing.T
	svc     *Service
	content *domain.Content
	now     int64
	serial  int
}

func newJourneyFixture(t *testing.T, encounter int) *journeyFixture {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "journey.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f, err := os.Open("../../content/test.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := domain.ReadContent(f)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Import(c); err != nil {
		t.Fatal(err)
	}
	j := &journeyFixture{t: t, svc: New(store, c.Version), content: c, now: 100}
	j.svc.Now = func() time.Time { return time.Unix(j.now, 0) }
	r := &domain.Room{ID: "journey", Code: "JOURNEY", Leader: "1", ContentVersion: c.Version, Phase: "lobby", LastAction: j.now, Players: []domain.Fighter{
		{ID: "1", Name: "One", Class: "warrior", Background: "miner", Ready: true},
		{ID: "2", Name: "Two", Class: "ranger", Background: "wanderer", Ready: true},
	}}
	if err = r.Start(c, j.now); err != nil {
		t.Fatal(err)
	}
	r.Encounter = encounter
	r.Enter(c, j.now)
	if err = store.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, p := range r.Players {
			if _, err := tx.Exec("INSERT INTO users(id,adult,room) VALUES(?,1,?)", p.ID, r.ID); err != nil {
				return err
			}
		}
		if err := db.Save(tx, r, c); err != nil {
			return err
		}
		return j.svc.broadcast(tx, r, c)
	}); err != nil {
		t.Fatal(err)
	}
	return j
}
func (j *journeyFixture) room() *domain.Room {
	j.t.Helper()
	var r *domain.Room
	if err := j.svc.Store.Tx(context.Background(), func(tx *sql.Tx) error { var err error; r, err = db.Room(tx, "journey"); return err }); err != nil {
		j.t.Fatal(err)
	}
	return r
}
func (j *journeyFixture) send(user, text string) {
	j.t.Helper()
	j.serial++
	if err := j.svc.Handle(context.Background(), Input{ID: fmt.Sprint(j.serial), User: user, Text: text}); err != nil {
		j.t.Fatal(err)
	}
}
func (j *journeyFixture) button(user, action string) string {
	j.t.Helper()
	var id string
	if err := j.svc.Store.DB.QueryRow("SELECT id FROM buttons WHERE user=? AND room='journey' AND phase=? AND action=? ORDER BY rowid DESC LIMIT 1", user, j.room().PhaseID, action).Scan(&id); err != nil {
		j.t.Fatal(action, err)
	}
	return id
}
func (j *journeyFixture) callback(user, id, update string) {
	j.t.Helper()
	if err := j.svc.Handle(context.Background(), Input{ID: update, User: user, Callback: id}); err != nil {
		j.t.Fatal(err)
	}
}

func TestFinalVoteCallbacksPersistAndReachBothOutcomes(t *testing.T) {
	for _, join := range []bool{true, false} {
		t.Run(fmt.Sprint(join), func(t *testing.T) {
			j := newJourneyFixture(t, 8)
			j.send("1", "/confirm")
			j.send("2", "/confirm")
			j.send("1", "/continue")
			if j.room().Phase != "vote" {
				t.Fatal(j.room().Phase)
			}
			action := "/vote no"
			if join {
				action = "/vote yes"
			}
			first := j.button("1", action)
			second := j.button("2", action)
			j.callback("2", first, "foreign-vote")
			if len(j.room().Votes) != 0 {
				t.Fatal("foreign button voted")
			}
			j.callback("1", first, "vote-first")
			if len(j.room().Votes) != 1 || j.room().Phase != "vote" {
				t.Fatal("first vote not saved")
			}
			// Recreate the application over the saved SQLite state before the deciding vote.
			j.svc = New(j.svc.Store, j.content.Version)
			j.svc.Now = func() time.Time { return time.Unix(j.now, 0) }
			j.callback("1", first, "vote-first")
			j.callback("1", first, "vote-first-redelivered")
			j.callback("2", second, "vote-second")
			r := j.room()
			if join && (r.Phase != "ended" || r.Outcome != "joined") {
				t.Fatal(r.Phase, r.Outcome)
			}
			if !join && (r.Phase != "combat" || len(r.Enemies) == 0 || r.Round != 1) {
				t.Fatal("final fight did not start", r.Phase)
			}
			snapshot, _ := json.Marshal(r)
			j.callback("2", second, "vote-second")
			after, _ := json.Marshal(j.room())
			if string(snapshot) != string(after) {
				t.Fatal("duplicate vote changed final state")
			}
			sender := &fake{messages: map[string][]Message{}}
			for {
				worked, err := j.svc.Deliver(context.Background(), sender, func(error) (time.Duration, bool) { return time.Second, false })
				if err != nil {
					t.Fatal(err)
				}
				if !worked {
					break
				}
			}
			for _, user := range []string{"1", "2"} {
				messages := sender.messages[user]
				if len(messages) == 0 {
					t.Fatal("no delivery")
				}
				text := messages[len(messages)-1].Text
				if join && !strings.Contains(strings.ToLower(text), "сторона босса принята") && !strings.Contains(text, "joined") {
					t.Fatal("missing ending", text)
				}
				if !join && !strings.Contains(text, j.content.Enemy(j.room().Enemies[0].Def).Name) {
					t.Fatal("missing boss", text)
				}
			}
		})
	}
}

func TestMemoryConfirmationGateDraftChangesPauseAndTimeout(t *testing.T) {
	j := newJourneyFixture(t, 2)
	phase := j.room().PhaseID
	j.send("1", "/continue")
	j.send("1", "/fight")
	if j.room().PhaseID != phase {
		t.Fatal("preparation bypassed confirmation")
	}
	j.send("1", "/confirm")
	j.send("1", "/continue")
	if j.room().PhaseID != phase {
		t.Fatal("leader bypassed other player's confirmation")
	}
	j.send("2", "/confirm")
	j.send("2", "/memory empty")
	if j.room().MemoryReady() {
		t.Fatal("draft edit did not revoke confirmation")
	}
	j.send("1", "/pause")
	j.now += 10
	j.send("1", "/resume")
	if len(j.room().Players[1].Draft) != 0 || j.room().MemoryReady() {
		t.Fatal("pause lost pending draft")
	}
	oldMemory := j.room().Players[1].Memory[0]
	j.now = j.room().Deadline
	if err := j.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := j.room()
	if r.Phase != "challenge" || len(r.Players[1].Memory) != 1 || r.Players[1].Memory[0] != oldMemory {
		t.Fatal("timeout committed unconfirmed draft", r.Phase)
	}
}

func TestFinalVoteDeadlineConcurrentWithLastCallback(t *testing.T) {
	j := newJourneyFixture(t, 8)
	j.send("1", "/confirm")
	j.send("2", "/confirm")
	j.send("1", "/continue")
	j.callback("1", j.button("1", "/vote yes"), "first")
	last := j.button("2", "/vote yes")
	j.now = j.room().Deadline
	var wg sync.WaitGroup
	for _, run := range []func() error{
		func() error { return j.svc.Tick(context.Background()) },
		func() error { return j.svc.Handle(context.Background(), Input{ID: "late", User: "2", Callback: last}) },
	} {
		wg.Add(1)
		go func(run func() error) {
			defer wg.Done()
			if err := run(); err != nil {
				t.Error(err)
			}
		}(run)
	}
	wg.Wait()
	r := j.room()
	if r.Phase != "combat" || r.Round != 1 || r.Outcome != "" {
		t.Fatal("late vote applied or fight restarted", r.Phase, r.Round)
	}
}
