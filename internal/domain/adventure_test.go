package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

func currentFixture(t *testing.T, count int) (*Content, *Room) {
	t.Helper()
	f, err := os.Open("../../content/test.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := ReadContent(f)
	if err != nil {
		t.Fatal(err)
	}
	r := &Room{Phase: "lobby", Leader: "0", LastAction: 100}
	for i := 0; i < count; i++ {
		r.Players = append(r.Players, Fighter{ID: fmt.Sprint(i), Name: fmt.Sprint(i), Class: "warrior", Background: "miner", Ready: true})
	}
	if err := r.Start(c, &sequence{values: []int{2, 2, 1}}, 100); err != nil {
		t.Fatal(err)
	}
	return c, r
}

func TestBarrelsCountPeopleAndFinalCarriesHealth(t *testing.T) {
	for _, count := range []int{2, 3, 7} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, r := currentFixture(t, count)
			for stage, enc := range []int{2, 5, 6} {
				r.Encounter = enc
				r.Players[0].HP = 0
				r.Enter(c, 101)
				if r.Phase != "barrel" || r.Players[0].HP != 0 {
					t.Fatal("automatic heal")
				}
				for i := range r.Players {
					if err := r.ChooseBarrel(c, r.Players[i].ID, true, 102); err != nil {
						t.Fatal(err)
					}
					if err := r.ChooseBarrel(c, r.Players[i].ID, true, 102); err == nil {
						t.Fatal("repeated drink")
					}
				}
				if r.Drinks != (stage+1)*count || r.Players[0].HP != r.Players[0].Stats(c).HP || r.Phase != "preparation" {
					t.Fatal("barrel count/revival", r.Drinks)
				}
			}
			r.FinishMemory()
			if err := r.Continue(c, &sequence{}, 103); err != nil {
				t.Fatal(err)
			}
			for _, p := range r.Players {
				if err := r.CastVote(c, p.ID, false, 104); err != nil {
					t.Fatal(err)
				}
			}
			if r.FinalStage != "minions" || len(r.Enemies) != 3*count {
				t.Fatal("minion count", len(r.Enemies))
			}
			for _, e := range r.Enemies {
				if e.HP != c.Enemy(c.Finale.Minion).HP {
					t.Fatal("double scaling")
				}
			}
			r.Players[0].HP = 0
			r.Players[1].HP = 4
			r.Players[1].Memory = []string{"warrior_attack_common"}
			r.Players[1].Temp = 5
			r.Players[1].Effects = []Timed{{Kind: "poison", Power: 1, Left: 2, End: "players"}}
			for i := range r.Enemies {
				r.Enemies[i].HP = 0
			}
			phase, slots := r.PhaseID, r.Players[1].Slots
			if !r.Check(c, 105) {
				t.Fatal("minion combat unfinished")
			}
			if r.Phase != "combat" || r.FinalStage != "alchemist" || r.PhaseID != phase+1 || r.Round != 1 || r.Outcome != "" || len(r.Enemies) != 1 {
				t.Fatal("final transition", r)
			}
			if r.Players[0].HP != 0 || r.Players[1].HP != 4 || len(r.Players[1].Offer) != 0 || r.Players[1].Slots != slots || r.Players[1].Temp != 0 || len(r.Players[1].Effects) != 0 {
				t.Fatal("unexpected reward/heal/effects")
			}
			r.Enemies[0].HP = 0
			r.Check(c, 106)
			if r.Outcome != "slain" {
				t.Fatal(r.Outcome)
			}
		})
	}
}

func TestBarrelTimeoutRefusalAndZeroMinions(t *testing.T) {
	c, r := currentFixture(t, 3)
	r.Encounter = 6
	r.Players[0].HP = 3
	r.Players[1].HP = 0
	r.Players[2].Absent = true
	r.Enter(c, 101)
	if err := r.ChooseBarrel(c, "missing", true, 102); err == nil {
		t.Fatal("unknown participant")
	}
	if err := r.ChooseBarrel(c, "2", true, 102); err == nil {
		t.Fatal("absent drank")
	}
	if err := r.ChooseBarrel(c, "0", false, 102); err != nil {
		t.Fatal(err)
	}
	r.Timeout(c, &sequence{}, r.Deadline)
	if r.Drinks != 0 || r.Players[0].HP != 3 || r.Players[1].HP != 0 || r.Phase != "preparation" {
		t.Fatal("timeout healed or drank")
	}
	r.FinishMemory()
	_ = r.Continue(c, &sequence{}, 200)
	r.Timeout(c, &sequence{}, r.Deadline)
	if r.FinalStage != "alchemist" || len(r.Enemies) != 1 || r.Enemies[0].Def != "alchemist" {
		t.Fatal("zero minion route")
	}
}

func TestBossChoiceSurvivesSnapshotAndStoryIsSceneSpecific(t *testing.T) {
	c, r := currentFixture(t, 2)
	if r.BossChoices["enc2"] != "brynda" || r.BossChoices["enc5"] != "zakvas" {
		t.Fatal(r.BossChoices)
	}
	r.Encounter = 5
	r.Enter(c, 101)
	r.FinishBarrel(c, 102)
	if err := r.TellStory(c, &sequence{}, "0"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.Log, "\n"), "питательной средой") {
		t.Fatal("missing professor reply")
	}
	if err := r.TellStory(c, &sequence{}, "0"); err == nil {
		t.Fatal("story repeated")
	}
	b, _ := json.Marshal(r)
	var restored Room
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	restored.BeginCombat(c, 103)
	if restored.Enemies[0].Def != "zakvas" || restored.CurrentEncounter(c).Name != "Профессор Заквас" || restored.Speech != 1 {
		t.Fatal("rerolled after restore")
	}
	if err := restored.TellStory(c, &sequence{}, "0"); err == nil {
		t.Fatal("combat story")
	}
}

func alchemistFixture(t *testing.T) (*Content, *Room) {
	c, r := currentFixture(t, 2)
	r.Encounter = 6
	r.BeginFinal(c, 101)
	for i := range r.Players {
		r.Players[i].Class = "ranger"
		r.Players[i].Memory = nil
	}
	return c, r
}

func TestAlchemistNormalAndCopiedAttackRespectInterception(t *testing.T) {
	c, r := alchemistFixture(t)
	r.Players[0].Memory = []string{"wizard_attack_common"}
	r.Players[1].Class = "warrior"
	r.Players[1].Memory = []string{"intercept_common", "counter_common"}
	before0, before1 := r.Players[0].HP, r.Players[1].HP
	r.EndPlayers(c, &sequence{}, 102)
	if r.Players[0].HP >= before0 || r.Players[1].HP >= before1 {
		t.Fatal("normal and distinct copied targets not hit")
	}
	log := strings.Join(r.Log, "\n")
	if strings.Count(log, "перехватывает") != 1 || strings.Count(log, "контратака") != 1 || !strings.Contains(log, "заимствует") {
		t.Fatal(log)
	}
	if !slices.Contains(r.Players[0].Memory, "wizard_attack_common") {
		t.Fatal("stolen memory")
	}
}

func TestAlchemistSkipAndLethalCounterPreventCopy(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		c, r := alchemistFixture(t)
		r.Players[0].Memory = []string{"ranger_attack_common"}
		r.Enemies[0].Effects = []Timed{{Kind: "skip"}}
		before := r.Players[0].HP
		r.EndPlayers(c, &sequence{}, 102)
		if r.Players[0].HP != before || strings.Contains(strings.Join(r.Log, "\n"), "заимствует") {
			t.Fatal("skip allowed action")
		}
	})
	t.Run("counter", func(t *testing.T) {
		c, r := alchemistFixture(t)
		r.Players[0].Class = "warrior"
		r.Players[0].Memory = []string{"counter_common", "ranger_attack_common"}
		r.Enemies[0].HP = 1
		r.EndPlayers(c, &sequence{}, 102)
		if r.Outcome != "slain" || strings.Contains(strings.Join(r.Log, "\n"), "заимствует") {
			t.Fatal("dead boss copied")
		}
	})
}

func TestCopiedHealOnceAndEffectsUseEnemySide(t *testing.T) {
	t.Run("heal", func(t *testing.T) {
		c, r := alchemistFixture(t)
		r.Players[0].Memory = []string{"heal_common", "resolve_common", "counter_common"}
		r.Enemies[0].HP = 1
		r.copyMemory(c, &sequence{}, &r.Enemies[0])
		if r.Enemies[0].HP != 13 {
			t.Fatal(r.Enemies[0].HP)
		} // 35% of 36, rounded down.
		b, _ := json.Marshal(r)
		var restored Room
		_ = json.Unmarshal(b, &restored)
		restored.copyMemory(c, &sequence{}, &restored.Enemies[0])
		if restored.Enemies[0].HP != 13 || len(restored.Players[0].Memory) != 3 {
			t.Fatal("heal copied twice or consumed hero memory")
		}
	})
	t.Run("shield", func(t *testing.T) {
		c, r := alchemistFixture(t)
		r.Players[0].Memory = []string{"warrior_attack_common"}
		r.copyMemory(c, &sequence{}, &r.Enemies[0])
		if r.Enemies[0].Temp != 3 {
			t.Fatal("no copied shield")
		}
		r.Players[0].Memory = nil
		r.EndPlayers(c, &sequence{}, 102)
		if r.Enemies[0].Temp != 0 {
			t.Fatal("enemy shield did not expire after player phase")
		}
	})
	t.Run("poison", func(t *testing.T) {
		c, r := alchemistFixture(t)
		var poison Memory
		for _, m := range c.Memories {
			for _, e := range m.Effects {
				if m.Rarity == "common" && e.Kind == "poison" {
					poison = m
				}
			}
		}
		r.Players[0].Memory = []string{poison.ID}
		r.copyMemory(c, &sequence{}, &r.Enemies[0])
		if len(r.Players[0].Effects) == 0 || r.Players[0].Effects[0].End != "players" {
			t.Fatal("copied poison uses wrong clock")
		}
		before := r.Players[0].HP
		r.tickEffects("0", &r.Players[0].HP, &r.Players[0].Temp, &r.Players[0].Effects, "players")
		if r.Players[0].HP >= before {
			t.Fatal("poison did not tick")
		}
	})
}

func TestNewContentRejectsBrokenPoolsAndFinale(t *testing.T) {
	for _, breakContent := range []func(*Content){
		func(c *Content) { c.Route[2].BossPool = []string{"missing"} },
		func(c *Content) { c.Route[2].BossPool = []string{"brok", "brok"} },
		func(c *Content) { c.Finale.Minion = "missing" },
		func(c *Content) { c.Route[6].Enemies = []string{"guard"} },
		func(c *Content) { c.Route = c.Route[:6] },
	} {
		c, _ := currentFixture(t, 2)
		breakContent(c)
		if c.Validate() == nil {
			t.Fatal("broken new content accepted")
		}
	}
}

func TestCopiedBoostRefreshesAndSkipStopsNextPlayerAction(t *testing.T) {
	t.Run("boost", func(t *testing.T) {
		c, r := alchemistFixture(t)
		r.Players[0].Memory = []string{"boost_common"}
		before := r.Players[0].HP
		r.EndPlayers(c, &sequence{}, 102)
		if r.Players[0].HP != before-3 {
			t.Fatal("boost applied before regular hit")
		}
		r.EndPlayers(c, &sequence{}, 103)
		if r.Players[0].HP != before-9 || len(r.Enemies[0].Effects) != 1 {
			t.Fatal("boost did not affect attack or stacked with itself")
		}
		r.Players[0].Memory = nil
		r.EndPlayers(c, &sequence{}, 104)
		if len(r.Enemies[0].Effects) != 0 || r.Players[0].HP != before-15 {
			t.Fatal("boost expiration")
		}
	})
	t.Run("skip", func(t *testing.T) {
		c, r := alchemistFixture(t)
		for _, m := range c.Memories {
			for _, effect := range m.Effects {
				if effect.Kind == "skip" && m.Rarity == "common" {
					r.Players[0].Memory = []string{m.ID}
				}
			}
		}
		r.copyMemory(c, &sequence{}, &r.Enemies[0])
		r.NewRound(c, 102)
		if !r.Players[0].Acted || r.Players[1].Acted {
			t.Fatal("copied skip did not consume next action")
		}
		if err := r.Act(c, &sequence{}, "0", r.Players[0].Memory[0], "0", 103); err == nil {
			t.Fatal("skipped player acted")
		}
	})
}
