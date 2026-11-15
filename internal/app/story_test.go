package app

import (
	"context"
	"encoding/json"
	db "poisoned-amber/internal/adapters/sqlite"
	"strings"
	"sync"
	"testing"
	"time"
)

type countedStoryRNG struct{ calls int }

func (r *countedStoryRNG) Intn(int) int { r.calls++; return 0 }

func TestStoryConcurrentButtonsAndReopenedSQLiteKeepChosenPair(t *testing.T) {
	j := journeyFixtureFrom(t, 5, "../../content/test.json")
	r := j.room()
	r.BossChoices["enc5"] = "zakvas"
	r.Players[1].Background = "miner"
	r.FinishBarrel(j.content, j.now)
	saveJourney(j, r)
	first := j.button("1", "/story")
	j.send("1", "/sync")
	second := j.button("1", "/story")
	rng := &countedStoryRNG{}
	j.svc.RNG = rng
	j.callback("2", first, "foreign-story")
	if rng.calls != 0 {
		t.Fatal("foreign button rolled")
	}
	var wg sync.WaitGroup
	for i, token := range []string{first, second} {
		wg.Add(1)
		go func(i int, token string) {
			defer wg.Done()
			err := j.svc.Handle(context.Background(), Input{ID: []string{"story-one", "story-two"}[i], User: "1", Callback: token})
			if err != nil {
				t.Error(err)
			}
		}(i, token)
	}
	wg.Wait()
	r = j.room()
	if rng.calls != 1 || len(r.StoryChoices) != 1 || len(r.Players[0].Flags) != 1 {
		t.Fatal("duplicate story effect", rng.calls, r.StoryChoices)
	}
	chosen := r.StoryChoices["5:1"]
	pool := r.CurrentEncounter(j.content).Dialogues["miner"]
	if chosen != pool[0].ID {
		t.Fatal("controlled RNG ignored")
	}
	for _, user := range []string{"1", "2"} {
		found := false
		for _, m := range queuedMessages(t, j.svc, user) {
			if strings.Contains(m.Text, esc(pool[0].Line)) && strings.Contains(m.Text, esc(pool[0].Response)) {
				found = true
				assertHTML(t, m.Text)
			}
		}
		if !found {
			t.Fatal("matching dialogue not queued for team", user)
		}
	}
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
	j.svc.RNG = rng
	j.svc.Now = func() time.Time { return time.Unix(j.now, 0) }
	before, _ := json.Marshal(j.room())
	j.callback("1", first, "story-after-restart")
	j.send("1", "/sync")
	after, _ := json.Marshal(j.room())
	if string(before) != string(after) || rng.calls != 1 {
		t.Fatal("restart/sync rerolled story")
	}
	j.callback("2", j.button("2", "/story"), "another-miner")
	r = j.room()
	if rng.calls != 2 || r.StoryChoices["5:2"] == chosen {
		t.Fatal("same-background player repeated available dialogue")
	}
}

func TestStoryAfterPreparationDeadlineDoesNotSetFlagOrChoice(t *testing.T) {
	j := journeyFixtureFrom(t, 5, "../../content/test.json")
	r := j.room()
	r.FinishBarrel(j.content, j.now)
	saveJourney(j, r)
	button := j.button("1", "/story")
	j.now = r.Deadline
	j.callback("1", button, "late-story")
	r = j.room()
	if r.Phase == "preparation" || len(r.StoryChoices) != 0 || len(r.Players[0].Flags) != 0 {
		t.Fatal("late story affected next phase")
	}
}
