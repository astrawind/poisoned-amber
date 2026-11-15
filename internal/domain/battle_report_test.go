package domain

import (
	"strings"
	"testing"
)

func TestCombatReportExplainsMissShieldAndDamage(t *testing.T) {
	c, r := fixture(t)
	p := &r.Players[0]
	if err := r.Act(c, &sequence{values: []int{99}}, p.ID, p.Memory[0], "0", 101); err != nil {
		t.Fatal(err)
	}
	report := strings.Join(r.Log, "\n")
	if !strings.Contains(report, "промах") || !strings.Contains(report, "+3 временных HP") {
		t.Fatal(report)
	}
	// Report actual HP loss separately from incoming damage absorbed by a shield.
	hp, shield := 10, 3
	r.dealDamage("Враг", "A", &hp, &shield, 5)
	if hp != 8 || shield != 0 || !strings.Contains(strings.Join(r.Log, "\n"), "потеря HP 2 · поглощено 3 · осталось HP 8") {
		t.Fatal(r.Log)
	}
	// A poison tick can expire and be absorbed without causing any HP loss.
	effects := []Timed{{Kind: "poison", Power: 2, Left: 1, End: "players"}}
	shield = 3
	r.tickEffects("A", &hp, &shield, &effects, "players")
	if hp != 8 || shield != 1 || len(effects) != 0 || !strings.Contains(strings.Join(r.Log, "\n"), "тик яда · потеря HP 0 · поглощено 2") {
		t.Fatal(r.Log)
	}
}

func TestTimeoutReportDoesNotRepeatPreviousRound(t *testing.T) {
	c, r := fixture(t)
	r.Log = []string{"previous-round-event"}
	r.Timeout(c, &sequence{}, r.Deadline)
	report := strings.Join(r.Log, "\n")
	if strings.Contains(report, "previous-round-event") || !strings.Contains(report, "A: время истекло") || !strings.Contains(report, "B: время истекло") {
		t.Fatal(report)
	}
	if r.Round != 2 {
		t.Fatal("timeout did not advance the round")
	}
}
