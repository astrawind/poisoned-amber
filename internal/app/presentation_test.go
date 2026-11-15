package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"io"
	"poisoned-amber/internal/domain"
	"strings"
	"testing"
)

func queuedMessages(t *testing.T, svc *Service, user string) []Message {
	t.Helper()
	rows, err := svc.Store.DB.Query("SELECT body FROM outbox WHERE chat=? ORDER BY id", user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		var raw []byte
		var m Message
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return messages
}
func assertHTML(t *testing.T, text string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader("<root>" + text + "</root>"))
	for {
		_, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid HTML: %v\n%s", err, text)
		}
	}
}
func TestSecretIsProminentEscapedAndPrivate(t *testing.T) {
	j := newJourneyFixture(t, 2)
	r := j.room()
	r.Phase = "challenge"
	r.Players[0].Name = "<Alice & Bob>"
	r.Challenge = domain.Challenge{Kind: "three_p", Performer: "1", Words: []string{"<секрет & молот>"}}
	if _, err := j.svc.Store.DB.Exec("DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	if err := j.svc.Store.Tx(context.Background(), func(tx *sql.Tx) error { return j.svc.broadcast(tx, r, j.content) }); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"1", "2"} {
		ms := queuedMessages(t, j.svc, user)
		if len(ms) != 1 {
			t.Fatal(len(ms))
		}
		m := ms[0]
		assertHTML(t, m.Text)
		if !m.HTML || !m.Fresh || !m.Card {
			t.Fatal("new event must be a new formatted card")
		}
		if strings.Contains(m.Text, "<Alice") {
			t.Fatal("name injected HTML")
		}
		if user == "1" && !strings.Contains(m.Text, "<b>«&lt;секрет &amp; молот&gt;»</b>") {
			t.Fatal("secret is not highlighted")
		}
		if user == "2" && strings.Contains(m.Text, "молот") {
			t.Fatal("secret leaked")
		}
	}
}
func TestLongScreensKeepAllEventsAndActionsWithinTelegramLimit(t *testing.T) {
	j := newJourneyFixture(t, 2)
	r := j.room()
	r.Log = nil
	for i := 0; i < 40; i++ {
		r.Log = append(r.Log, strings.Repeat("🔥", 120)+" & <удар>")
	}
	if _, err := j.svc.Store.DB.Exec("DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	if err := j.svc.Store.Tx(context.Background(), func(tx *sql.Tx) error { return j.svc.render(tx, r, j.content, &r.Players[0]) }); err != nil {
		t.Fatal(err)
	}
	ms := queuedMessages(t, j.svc, "1")
	if len(ms) < 2 {
		t.Fatal("expected split screen")
	}
	var joined string
	for i, m := range ms {
		if messageUnits(m.Text) > 3800 {
			t.Fatal("screen too long", messageUnits(m.Text))
		}
		assertHTML(t, m.Text)
		joined += m.Text
		if i < len(ms)-1 && (len(m.Buttons) > 0 || m.Card) {
			t.Fatal("only final chunk may carry controls")
		}
		if i == len(ms)-1 && (!m.Card || !m.Fresh || len(m.Buttons) == 0) {
			t.Fatal("missing final controls or chronological fresh screen")
		}
	}
	if strings.Count(joined, "&lt;удар&gt;") != 40 {
		t.Fatal("lost events")
	}
}
func TestSyncEditsLatestCardAndMemoryDraftStaysPrivate(t *testing.T) {
	j := newJourneyFixture(t, 2)
	if _, err := j.svc.Store.DB.Exec("DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	j.send("1", "/sync")
	m := queuedMessages(t, j.svc, "1")
	if len(m) != 1 || m[0].Fresh || !m[0].Card {
		t.Fatal("sync must refresh current screen")
	}
	if _, err := j.svc.Store.DB.Exec("DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	j.send("1", "/toggle warrior_attack_common")
	if len(queuedMessages(t, j.svc, "2")) != 0 {
		t.Fatal("draft update broadcast to other player")
	}
	m = queuedMessages(t, j.svc, "1")
	if len(m) != 1 || m[0].Fresh {
		t.Fatal("draft must edit personal screen")
	}
}
func TestOnboardingIncludesHelpAfterJoining(t *testing.T) {
	j := newJourneyFixture(t, 0)
	if _, err := j.svc.Store.DB.Exec("DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	j.send("1", "/start")
	ms := queuedMessages(t, j.svc, "1")
	if len(ms) < 2 || !strings.Contains(ms[0].Text, "/create") || !strings.Contains(ms[0].Text, "/leave") || !strings.Contains(ms[0].Text, "поставки") {
		t.Fatal("start must show onboarding, commands and current game")
	}
}
