package domain

import (
	"os"
	"slices"
	"testing"
)

type sequence struct {
	values []int
	i      int
}

func (r *sequence) Intn(n int) int {
	v := 0
	if r.i < len(r.values) {
		v = r.values[r.i]
		r.i++
	}
	return v % n
}
func fixture(t *testing.T) (*Content, *Room) {
	t.Helper()
	f, e := os.Open("../../content/test.json")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c, e := ReadContent(f)
	if e != nil {
		t.Fatal(e)
	}
	r := &Room{Phase: "lobby", Leader: "a", LastAction: 100, Players: []Fighter{{ID: "a", Name: "A", Class: "warrior", Background: "miner", Ready: true}, {ID: "b", Name: "B", Class: "ranger", Background: "wanderer", Ready: true}}}
	if e = r.Start(c, 100); e != nil {
		t.Fatal(e)
	}
	return c, r
}
func TestMemoryHPAndNoFreeAttack(t *testing.T) {
	c, r := fixture(t)
	p := &r.Players[0]
	p.HP = 6
	base := p.Stats(c).HP
	p.Memory = append(p.Memory, "resolve_common")
	p.Clamp(c)
	if p.HP != 6 || p.Stats(c).HP != base+5 {
		t.Fatal("bonus healed")
	}
	p.HP = base + 4
	p.Memory = p.Memory[:1]
	p.Clamp(c)
	if p.HP != base {
		t.Fatal("clamp")
	}
	p.HP = 0
	p.Memory = append(p.Memory, "resolve_common")
	p.Clamp(c)
	if p.HP != 0 {
		t.Fatal("revived")
	}
	p.HP = 10
	r.Phase = "preparation"
	if e := r.SetMemory(c, p, []string{"resolve_common"}); e != nil {
		t.Fatal(e)
	}
	r.BeginCombat(c, 101)
	if e := r.Act(c, &sequence{}, p.ID, "warrior_attack_common", "0", 102); e == nil || p.Acted {
		t.Fatal("free attack")
	}
}
func TestMissStillGrantsShieldAndStaleTarget(t *testing.T) {
	c, r := fixture(t)
	p := &r.Players[0]
	if e := r.Act(c, &sequence{}, p.ID, p.Memory[0], "missing", 101); e == nil || p.Acted {
		t.Fatal("stale target consumed action")
	}
	if e := r.Act(c, &sequence{values: []int{99}}, p.ID, p.Memory[0], "0", 101); e != nil {
		t.Fatal(e)
	}
	if p.Temp != 3 {
		t.Fatal(p.Temp)
	}
}
func TestPoisonStackRefreshAndShield(t *testing.T) {
	es := []Timed{}
	apply(&es, Timed{Kind: "poison", Source: "a", Family: "p", Power: 5, Left: 2, End: "players"})
	apply(&es, Timed{Kind: "poison", Source: "a", Family: "p", Power: 2, Left: 2, End: "players"})
	apply(&es, Timed{Kind: "poison", Source: "b", Family: "p", Power: 3, Left: 2, End: "players"})
	hp, tmp := 10, 7
	tick(&hp, &tmp, &es, "players")
	if hp != 9 || len(es) != 2 {
		t.Fatal(hp, es)
	}
	tick(&hp, &tmp, &es, "players")
	if hp != 1 || len(es) != 0 {
		t.Fatal(hp, es)
	}
}
func TestHealConsumesOwnBonus(t *testing.T) {
	c, r := fixture(t)
	p := &r.Players[0]
	p.Memory = []string{"heal_legendary"}
	p.HP = 20
	maxHP := p.Stats(c).HP
	if e := r.Act(c, &sequence{}, p.ID, p.Memory[0], "", 101); e != nil {
		t.Fatal(e)
	}
	if len(p.Memory) != 0 || p.HP != min(c.Class(p.Class).Base.HP, 20+maxHP*80/100) {
		t.Fatal(p)
	}
}
func TestChallengeRolesAndFinals(t *testing.T) {
	for _, kind := range []string{"crocodile", "three_p", "anagram", "codenames"} {
		t.Run(kind, func(t *testing.T) {
			c, r := fixture(t)
			c.Challenges = []string{kind}
			r.Encounter = 2
			r.StartChallenge(c, &sequence{}, 101)
			if kind == "codenames" {
				if len(r.Challenge.Words) != 3 || r.Challenge.Captain == r.Challenge.Guesser {
					t.Fatal(r.Challenge)
				}
				if e := r.ChallengeInput(c, r.Challenge.Captain, "", 1, 102); e == nil {
					t.Fatal("captain guessed")
				}
				for _, i := range []int{1, 2} {
					if e := r.ChallengeInput(c, r.Challenge.Guesser, "", i, 102); e != nil {
						t.Fatal(e)
					}
				}
			} else if kind == "anagram" {
				if r.Challenge.Scramble == r.Challenge.Words[0] {
					t.Fatal("not scrambled")
				}
				_ = r.ChallengeInput(c, "b", "  "+r.Challenge.Words[0]+" ", -1, 102)
			} else {
				if e := r.ChallengeInput(c, "b", "solved", -1, 102); e == nil {
					t.Fatal("secret role")
				}
				_ = r.ChallengeInput(c, r.Challenge.Performer, "solved", -1, 102)
			}
			if r.Phase != "rewards" || r.Bosses != 1 || r.Players[0].Slots != 3 {
				t.Fatal(r.Phase, r.Bosses)
			}
		})
	}
	for _, join := range []bool{true, false} {
		c, r := fixture(t)
		r.Encounter = 8
		r.Enter(c, 101)
		if e := r.Continue(c, &sequence{}, 102); e != nil {
			t.Fatal(e)
		}
		if r.Phase != "vote" {
			t.Fatal(r.Phase)
		}
		r.Votes = map[string]bool{"a": join, "b": join}
		r.FinishVote(c, 103)
		if join && r.Outcome != "joined" {
			t.Fatal(r.Outcome)
		}
		if !join {
			for i := range r.Enemies {
				r.Enemies[i].HP = 0
			}
			r.Check(c, 104)
			if r.Outcome != "slain" {
				t.Fatal(r.Outcome)
			}
		}
	}
	c, r := fixture(t)
	for i := range r.Players {
		r.Players[i].HP = 0
	}
	r.Check(c, 101)
	if r.Outcome != "defeat" {
		t.Fatal(r.Outcome)
	}
}
func TestFullRouteAndRewards(t *testing.T) {
	c, r := fixture(t)
	rng := &sequence{}
	for r.Encounter < 8 {
		for i := range r.Enemies {
			r.Enemies[i].HP = 0
		}
		r.Check(c, 110)
		r.Offers(c, rng)
		for _, p := range r.Players {
			for _, id := range p.Offer {
				if slices.Contains(p.Memory, id) {
					t.Fatal("owned offered")
				}
			}
		}
		r.Timeout(c, rng, r.Deadline)
		if e := r.Continue(c, rng, 200); e != nil {
			t.Fatal(e)
		}
		if r.Phase == "preparation" {
			if r.Players[0].HP != r.Players[0].Stats(c).HP {
				t.Fatal("barrel")
			}
			r.BeginCombat(c, 201)
		}
	}
	if r.Players[0].Slots != 4 || r.Bosses != 2 {
		t.Fatal(r.Bosses)
	}
}
func TestContentRejectsUnknown(t *testing.T) {
	c, _ := fixture(t)
	c.Memories[0].Effects[0].Kind = "script"
	if c.Validate() == nil {
		t.Fatal("unknown accepted")
	}
}

func TestWizardDistinctHitsAndWarriorReactions(t *testing.T) {
	c, r := fixture(t)
	p := &r.Players[0]
	p.Class = "wizard"
	p.Memory = []string{"wizard_attack_common"}
	before0, before1 := r.Enemies[0].HP, r.Enemies[1].HP
	if e := r.Act(c, &sequence{values: []int{0, 0, 99}}, p.ID, p.Memory[0], "0", 101); e != nil {
		t.Fatal(e)
	}
	if r.Enemies[0].HP >= before0 || r.Enemies[1].HP != before1 {
		t.Fatal("independent hit rolls")
	}
	c, r = fixture(t)
	p = &r.Players[0]
	p.Memory = []string{"counter_common", "counter_rare"}
	for i := range r.Players {
		r.Players[i].Acted = true
	}
	for i := range r.Enemies {
		apply(&r.Enemies[i].Effects, Timed{Kind: "taunt", Source: p.ID, Family: "taunt", Left: 2, End: "enemies"})
	}
	r.EndPlayers(c, &sequence{}, 102)
	if r.Enemies[0].HP >= 16 || r.Enemies[1].HP >= 16 {
		t.Fatal("separate reactions per enemy attack")
	}
	c, r = fixture(t)
	p = &r.Players[0]
	p.Memory = []string{"intercept_common", "counter_common"}
	r.Enemies = r.Enemies[:1]
	r.Enemies[0].Effects = []Timed{{Kind: "taunt", Source: "b", Left: 2, End: "enemies"}}
	hp := r.Players[1].HP
	r.EndPlayers(c, &sequence{}, 102)
	if r.Players[1].HP != hp || p.HP == p.Stats(c).HP {
		t.Fatal("intercept failed")
	}
	if r.Enemies[0].HP != 16 {
		t.Fatal("second reaction on same root")
	}
}

func TestVoteTimeoutDoesNotUseTieBreakForMissingVote(t *testing.T) {
	c, r := fixture(t)
	r.Encounter = 8
	r.Enter(c, 101)
	_ = r.Continue(c, &sequence{}, 102)
	r.Votes = map[string]bool{"a": true}
	r.Timeout(c, &sequence{}, r.Deadline)
	if r.Phase != "combat" {
		t.Fatal("missing vote must default to refusal")
	}
	c, r = fixture(t)
	r.Encounter = 8
	r.Enter(c, 101)
	_ = r.Continue(c, &sequence{}, 102)
	r.Votes = map[string]bool{"a": true, "b": false}
	r.FinishVote(c, 103)
	if r.Outcome != "joined" {
		t.Fatal("completed tie must use leader")
	}
}
