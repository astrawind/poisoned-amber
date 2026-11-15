package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	db "poisoned-amber/internal/adapters/sqlite"
	"poisoned-amber/internal/domain"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type zeroRNG struct{}

func (zeroRNG) Intn(int) int { return 0 }

func saveJourney(j *journeyFixture, r *domain.Room) {
	j.t.Helper()
	if err := j.svc.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		if err := db.Save(tx, r, j.content); err != nil {
			return err
		}
		return j.svc.broadcast(tx, r, j.content)
	}); err != nil {
		j.t.Fatal(err)
	}
}

func TestBarrelCallbacksPersistAcrossDatabaseReopen(t *testing.T) {
	j := journeyFixtureFrom(t, 6, "../../content/test.json")
	r := j.room()
	r.Players[0].HP = 0
	r.Players[1].HP = 5
	saveJourney(j, r)
	first, second := j.button("1", "/barrel yes"), j.button("2", "/barrel no")
	j.callback("2", first, "foreign-barrel")
	if j.room().Drinks != 0 {
		t.Fatal("foreign drink")
	}
	j.callback("1", first, "drink")
	j.callback("1", first, "drink")
	j.callback("1", first, "drink-redelivered")
	j.send("1", "/barrel yes")
	r = j.room()
	if r.Drinks != 1 || r.Players[0].HP != r.Players[0].Stats(j.content).HP || r.Phase != "barrel" {
		t.Fatal("duplicate or missing revival")
	}
	choices := r.BossChoices
	var seq int
	var name, path string
	if err := j.svc.Store.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := j.svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	j.svc = New(store, j.content.Version)
	j.svc.Now = func() time.Time { return time.Unix(j.now, 0) }
	j.callback("2", second, "refuse") // Still valid after another player's action and restart.
	r = j.room()
	if r.Phase != "preparation" || r.Drinks != 1 || r.Players[1].HP != 5 || !reflect.DeepEqual(choices, r.BossChoices) {
		t.Fatal("lost choice or healed refusal")
	}
	j.send("1", "/confirm")
	j.send("2", "/confirm")
	j.send("1", "/continue")
	j.send("1", "/vote no")
	j.send("2", "/vote no")
	r = j.room()
	if r.FinalStage != "minions" || len(r.Enemies) != 1 {
		t.Fatal("wrong minion count")
	}
	oldPhase := r.PhaseID
	oldAction := j.button("1", "/act warrior_attack_common "+r.Enemies[0].ID)
	j.svc.RNG = zeroRNG{}
	j.callback("1", oldAction, "last-minion")
	r = j.room()
	if r.FinalStage != "alchemist" || r.PhaseID != oldPhase+1 || r.Players[1].HP != 5 || r.Round != 1 {
		t.Fatal("transition healed/rewarded")
	}
	snapshot, _ := json.Marshal(r)
	j.callback("1", oldAction, "last-minion-redelivered")
	after, _ := json.Marshal(j.room())
	if string(snapshot) != string(after) {
		t.Fatal("old minion button hit alchemist")
	}
}

func TestBarrelDeadlineRacesCallbackAndPausePreservesChoices(t *testing.T) {
	j := journeyFixtureFrom(t, 2, "../../content/test.json")
	r := j.room()
	r.Players[0].HP = 2
	r.Players[1].HP = 0
	saveJourney(j, r)
	j.send("1", "/barrel no")
	j.send("1", "/pause")
	j.now += 10
	j.send("1", "/resume")
	if j.room().Phase != "barrel" || len(j.room().BarrelChoices) != 1 {
		t.Fatal("pause lost choice")
	}
	last := j.button("2", "/barrel yes")
	j.now = j.room().Deadline
	var wg sync.WaitGroup
	for _, run := range []func() error{
		func() error { return j.svc.Tick(context.Background()) },
		func() error {
			return j.svc.Handle(context.Background(), Input{ID: "late-drink", User: "2", Callback: last})
		},
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
	r = j.room()
	if r.Phase != "preparation" || r.Drinks != 0 || r.Players[1].HP != 0 || r.Players[0].HP != 2 {
		t.Fatal("late consent applied", r)
	}
	phase := r.PhaseID
	if err := j.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j.room().PhaseID != phase {
		t.Fatal("deadline repeated")
	}
}

func TestCurrentFullJourneyViaCommandsAndFakeDelivery(t *testing.T) {
	j := journeyFixtureFrom(t, 0, "../../content/test.json")
	j.svc.RNG = zeroRNG{}
	for steps := 0; steps < 150 && j.room().Phase != "ended"; steps++ {
		r := j.room()
		switch r.Phase {
		case "combat":
			var player *domain.Fighter
			for i := range r.Players {
				p := &r.Players[i]
				if p.HP > 0 && !p.Acted {
					player = p
					break
				}
			}
			if player == nil {
				t.Fatal("no available actor")
			}
			target := ""
			for _, e := range r.Enemies {
				if e.HP > 0 {
					target = e.ID
					break
				}
			}
			j.send(player.ID, "/act "+player.Memory[0]+" "+target)
		case "barrel":
			for _, p := range r.Players {
				j.send(p.ID, "/barrel yes")
			}
		case "rewards", "preparation":
			for _, p := range r.Players {
				if p.HP > 0 {
					j.send(p.ID, "/confirm")
				}
			}
			if r.Phase == "preparation" {
				j.send(r.Leader, "/continue")
			}
		case "transition":
			j.send(r.Leader, "/continue")
		case "challenge":
			ch := r.Challenge
			switch ch.Kind {
			case "crocodile", "three_p":
				j.send(ch.Performer, "/answer solved")
			case "anagram":
				j.send(ch.Performer, "/answer "+ch.Words[0])
			case "codenames":
				for i := range ch.Words {
					if i != ch.Black {
						j.send(ch.Guesser, fmt.Sprintf("/cell %d", i))
					}
				}
			}
		case "vote":
			for _, p := range r.Players {
				if p.HP > 0 {
					j.send(p.ID, "/vote no")
				}
			}
		default:
			t.Fatal("unexpected phase", r.Phase)
		}
	}
	r := j.room()
	if r.Outcome != "slain" || r.Encounter != 6 || r.Drinks != 6 || r.Bosses != 2 || r.Players[0].Slots != 4 {
		t.Fatal("full route", r)
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
			t.Fatal("no delivered messages")
		}
		for _, m := range messages {
			if m.HTML {
				assertHTML(t, m.Text)
			}
			if messageUnits(m.Text) > 3800 {
				t.Fatal("oversized message")
			}
		}
		if !strings.Contains(messages[len(messages)-1].Text, "Алхимик погиб") {
			t.Fatal("missing victory")
		}
	}
}

func TestContentUpgradeKeepsLegacyRoomAndImmutablePackages(t *testing.T) {
	j := newJourneyFixture(t, 8)
	f, err := os.Open("../../content/test.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	current, err := domain.ReadContent(f)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.svc.Store.Import(current); err != nil {
		t.Fatal(err)
	}
	if err = j.svc.Store.Import(j.content); err != nil {
		t.Fatal("legacy package changed on roundtrip", err)
	}
	j.svc.Version = current.Version
	j.send("1", "/confirm")
	j.send("2", "/confirm")
	j.send("1", "/continue")
	j.send("1", "/vote no")
	j.send("2", "/vote no")
	r := j.room()
	if r.ContentVersion != j.content.Version || r.FinalStage != "" || r.Enemies[0].Def != "boss" {
		t.Fatal("legacy room upgraded mid-game")
	}
	current.Finale.Speeches[0] = "changed"
	if err = j.svc.Store.Import(current); err == nil {
		t.Fatal("content changed under same version")
	}
}
