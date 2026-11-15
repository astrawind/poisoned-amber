package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type storyRNG struct{ calls, value int }

func (r *storyRNG) Intn(n int) int { r.calls++; return r.value % n }

func TestStoryDrawsMatchingPairsAndExhaustsPoolBeforeRepeating(t *testing.T) {
	c, r := currentFixture(t, 3)
	r.Encounter = 5
	r.Enter(c, 101)
	r.FinishBarrel(c, 102)
	rng := &storyRNG{value: 1}
	pool := r.CurrentEncounter(c).Dialogues["miner"]
	for i, id := range []string{"0", "1", "2"} {
		if err := r.TellStory(c, rng, id); err != nil {
			t.Fatal(err)
		}
		choice := r.StoryChoices["5:"+id]
		var selected Dialogue
		for _, d := range pool {
			if d.ID == choice {
				selected = d
			}
		}
		if selected.ID == "" || r.Log[len(r.Log)-2] != id+": "+selected.Line || r.Log[len(r.Log)-1] != selected.Response {
			t.Fatal("unmatched or wrong-scene dialogue")
		}
		if i == 1 && r.StoryChoices["5:0"] == choice {
			t.Fatal("repeated before pool exhausted")
		}
	}
	if rng.calls != 3 {
		t.Fatal("unexpected random draws", rng.calls)
	}
	before, _ := json.Marshal(r)
	if err := r.TellStory(c, rng, "0"); err == nil {
		t.Fatal("repeat accepted")
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) || rng.calls != 3 {
		t.Fatal("repeat mutated or rerolled")
	}
	var restored Room
	if err := json.Unmarshal(before, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.TellStory(c, rng, "0"); err == nil || rng.calls != 3 {
		t.Fatal("restart rerolled")
	}
	// A new scene uses its own bank, including a paired response from the alchemist.
	restored.Encounter = 6
	restored.Enter(c, 103)
	restored.FinishBarrel(c, 104)
	if err := restored.TellStory(c, rng, "0"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(restored.StoryChoices["6:0"], "alchemist.") || !strings.HasPrefix(restored.Log[len(restored.Log)-1], "Алхимик:") {
		t.Fatal("final scene used another boss's dialogue")
	}
	if len(restored.Players[0].Flags) != 1 {
		t.Fatal("duplicated story flag")
	}
}

func TestStoryLegacyFallbackDoesNotDrawRandomness(t *testing.T) {
	for _, schema := range []int{1, 2} {
		var c *Content
		var r *Room
		if schema == 1 {
			c, r = fixture(t)
		} else {
			c, r = currentFixture(t, 2)
			for i := range c.Bosses {
				c.Bosses[i].Dialogues = nil
				c.Bosses[i].Replies = map[string]string{"miner": "Старый ответ"}
			}
			for i := range c.Route {
				c.Route[i].Dialogues = nil
			}
		}
		r.Encounter = 2
		r.Enter(c, 101)
		if schema == 2 {
			r.FinishBarrel(c, 102)
		}
		rng := &storyRNG{}
		if err := r.TellStory(c, rng, r.Players[0].ID); err != nil {
			t.Fatal(err)
		}
		if rng.calls != 0 || len(r.StoryChoices) != 0 {
			t.Fatal("legacy reply drew a new variant")
		}
		if !strings.Contains(strings.Join(r.Log, "\n"), c.Background("miner").Reply) {
			t.Fatal("legacy line missing")
		}
		if schema == 2 && r.Log[len(r.Log)-1] != "Старый ответ" {
			t.Fatal("legacy boss response missing")
		}
	}
}

func TestCurrentDialogueBanksCoverEveryBackgroundWithoutCrossSceneRepeats(t *testing.T) {
	c, _ := currentFixture(t, 2)
	var banks []map[string][]Dialogue
	for _, b := range c.Bosses {
		banks = append(banks, b.Dialogues)
	}
	for _, e := range c.Route {
		if len(e.BossPool) == 0 {
			banks = append(banks, e.Dialogues)
		}
	}
	for _, bg := range c.Backgrounds {
		seen := map[string]bool{}
		for _, bank := range banks {
			if len(bank[bg.ID]) < 2 {
				t.Fatal("missing alternatives", bg.ID)
			}
			for _, d := range bank[bg.ID] {
				line := Normalize(d.Line)
				if seen[line] {
					t.Fatal("same line in different scenes", bg.ID, line)
				}
				seen[line] = true
			}
		}
	}
}

func TestContentRejectsBrokenDialogueReferencesAndPairs(t *testing.T) {
	for name, breakContent := range map[string]func(*Content){
		"unknown background":     func(c *Content) { c.Bosses[0].Dialogues["missing"] = c.Bosses[0].Dialogues["miner"] },
		"empty pool":             func(c *Content) { c.Bosses[0].Dialogues["miner"] = nil },
		"missing response":       func(c *Content) { c.Bosses[0].Dialogues["miner"][0].Response = " " },
		"missing line":           func(c *Content) { c.Route[0].Dialogues["miner"][0].Line = " " },
		"duplicate id":           func(c *Content) { c.Route[0].Dialogues["miner"][0].ID = c.Bosses[0].Dialogues["miner"][0].ID },
		"duplicate line":         func(c *Content) { c.Bosses[0].Dialogues["miner"][1].Line = c.Bosses[0].Dialogues["miner"][0].Line },
		"ambiguous pooled scene": func(c *Content) { c.Route[2].Dialogues = c.Route[0].Dialogues },
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := currentFixture(t, 2)
			breakContent(c)
			if c.Validate() == nil {
				t.Fatal("broken dialogue accepted")
			}
		})
	}
	// Story selection must not mutate the pinned content.
	c, r := fixture(t)
	before, _ := json.Marshal(c)
	r.Encounter = 2
	r.Enter(c, 101)
	_ = r.TellStory(c, &storyRNG{}, "a")
	after, _ := json.Marshal(c)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("story mutated content")
	}
}
