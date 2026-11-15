package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func drinkBanners(t *testing.T, svc *Service, user string) []Message {
	t.Helper()
	var banners []Message
	for _, m := range queuedMessages(t, svc, user) {
		if strings.Contains(m.Text, "<b>КРУЖКУ ВВЕРХ!</b>") {
			banners = append(banners, m)
		}
	}
	return banners
}

func TestAdultExplainsDrinkMomentsAndPreservesOnboarding(t *testing.T) {
	j := journeyFixtureFrom(t, 0, "../../content/test.json")
	if _, err := j.svc.Store.DB.Exec("UPDATE users SET adult=0 WHERE id='1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.svc.Store.DB.Exec("DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	j.send("1", "/start")
	before := queuedMessages(t, j.svc, "1")
	if len(before) != 1 || strings.Contains(before[0].Text, "ПОДГОТОВЬТЕ КРУЖКИ") {
		t.Fatal("drink prompt before age confirmation")
	}
	in := Input{ID: "adult-with-drinks", User: "1", Text: "/adult"}
	if err := j.svc.Handle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := j.svc.Handle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	ms := queuedMessages(t, j.svc, "1")
	if len(ms) != 2 {
		t.Fatal("duplicate adult response", len(ms))
	}
	m := ms[1]
	assertHTML(t, m.Text)
	for _, text := range []string{"ПОДГОТОВЬТЕ КРУЖКИ", "чай, сок и вода", "бочку", "лечебное воспоминание", "/create", "/join"} {
		if !strings.Contains(m.Text, text) {
			t.Fatal("missing onboarding information", text)
		}
	}
	if !m.HTML || m.Card || messageUnits(m.Text) > 3800 {
		t.Fatal("bad adult formatting")
	}
}

func TestAcceptedDrinkActionsQueueOneNamedBannerForTheTeam(t *testing.T) {
	for _, occasion := range []string{"barrel", "heal", "heal_at_full_hp"} {
		t.Run(occasion, func(t *testing.T) {
			encounter := 0
			if occasion == "barrel" {
				encounter = 2
			}
			j := journeyFixtureFrom(t, encounter, "../../content/test.json")
			r := j.room()
			r.Players[0].Name = "<Алиса & Боб>"
			action, reason := "/barrel yes", "Принято угощение из бочки"
			if occasion != "barrel" {
				r.Players[0].Memory = []string{"heal_common"}
				if occasion == "heal" {
					r.Players[0].HP = 1
				}
				action, reason = "/act heal_common", j.content.Memory("heal_common").Name
			}
			saveJourney(j, r)
			button := j.button("1", action)
			j.callback("2", button, "foreign-drink")
			if len(drinkBanners(t, j.svc, "1")) != 0 {
				t.Fatal("foreign action toasted")
			}
			j.callback("1", button, "accepted-drink")
			j.callback("1", button, "accepted-drink")
			j.callback("1", button, "redelivered-drink")
			j.send("1", action) // Already consumed, or a second answer at the same barrel.
			j.svc = New(j.svc.Store, j.content.Version)
			j.svc.Now = func() time.Time { return time.Unix(j.now, 0) }
			j.callback("1", button, "drink-after-restart")
			j.send("1", "/sync")
			j.send("2", "/sync")
			for _, user := range []string{"1", "2"} {
				banners := drinkBanners(t, j.svc, user)
				if len(banners) != 1 {
					t.Fatal("missing or repeated banner", user, len(banners))
				}
				m := banners[0]
				assertHTML(t, m.Text)
				if !m.HTML || !m.Fresh || m.Card || len(m.Buttons) != 0 || !strings.Contains(m.Text, "&lt;Алиса &amp; Боб&gt;") || !strings.Contains(m.Text, reason) {
					t.Fatal("bad named banner", m)
				}
				ms := queuedMessages(t, j.svc, user)
				if !ms[len(ms)-1].Card {
					t.Fatal("current controls displaced by banner")
				}
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
				count := 0
				for _, m := range sender.messages[user] {
					if strings.Contains(m.Text, "<b>КРУЖКУ ВВЕРХ!</b>") {
						count++
					}
				}
				if count != 1 {
					t.Fatal("fake delivery repeated toast", count)
				}
			}
		})
	}
}

func TestRefusalTimeoutShieldAndAlchemistHealingDoNotToastPlayers(t *testing.T) {
	for _, occasion := range []string{"refusal", "timeout", "shield", "alchemist"} {
		t.Run(occasion, func(t *testing.T) {
			encounter := 2
			if occasion == "shield" {
				encounter = 0
			}
			if occasion == "alchemist" {
				encounter = 6
			}
			j := journeyFixtureFrom(t, encounter, "../../content/test.json")
			j.svc.RNG = zeroRNG{}
			switch occasion {
			case "refusal":
				j.send("1", "/barrel no")
			case "timeout":
				j.now = j.room().Deadline
				j.callback("1", j.button("1", "/barrel yes"), "late-drink")
			case "shield":
				j.send("1", "/act warrior_attack_common 0")
				if j.room().Players[0].Temp == 0 {
					t.Fatal("shield was not applied")
				}
			case "alchemist":
				r := j.room()
				r.BeginFinal(j.content, j.now)
				r.Enemies[0].HP = 1
				r.Players[0].Memory = []string{"heal_common"}
				r.Players[1].Acted = true
				saveJourney(j, r)
				j.send("1", "/act skip")
				if j.room().Enemies[0].HP <= 1 {
					t.Fatal("alchemist did not heal")
				}
			}
			for _, user := range []string{"1", "2"} {
				if len(drinkBanners(t, j.svc, user)) != 0 {
					t.Fatal("unexpected toast", occasion)
				}
			}
		})
	}
}

func TestDrinkBannerAndGameEffectCommitTogether(t *testing.T) {
	j := journeyFixtureFrom(t, 2, "../../content/test.json")
	r := j.room()
	r.Players[0].HP = 1
	r.Players[1].Absent = true
	saveJourney(j, r)
	_, err := j.svc.Store.DB.Exec(`CREATE TRIGGER fail_drink_banner BEFORE INSERT ON outbox
 WHEN instr(NEW.body, 'КРУЖКУ ВВЕРХ!') > 0 BEGIN SELECT RAISE(ABORT, 'test banner failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{ID: "atomic-drink", User: "1", Text: "/barrel yes"}
	if err = j.svc.Handle(context.Background(), in); err == nil {
		t.Fatal("expected enqueue failure")
	}
	r = j.room()
	if r.Drinks != 0 || r.Players[0].HP != 1 || len(r.BarrelChoices) != 0 {
		t.Fatal("game committed without banner")
	}
	if _, err = j.svc.Store.DB.Exec("DROP TRIGGER fail_drink_banner"); err != nil {
		t.Fatal(err)
	}
	if err = j.svc.Handle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	r = j.room()
	if r.Drinks != 1 || r.Players[0].HP != r.Players[0].Stats(j.content).HP || len(drinkBanners(t, j.svc, "1")) != 1 {
		t.Fatal("retry did not commit exactly once")
	}
	if len(drinkBanners(t, j.svc, "2")) != 0 {
		t.Fatal("banner sent to absent player")
	}
}
