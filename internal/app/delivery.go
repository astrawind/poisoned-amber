package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type Sender interface {
	Send(context.Context, string, Message, int64) (int64, error)
}
type FailurePolicy func(error) (retry time.Duration, permanent bool)

// A single delivery worker preserves per-chat order. A delayed recipient does not block other chats.
func (s *Service) Deliver(ctx context.Context, sender Sender, policy FailurePolicy) (bool, error) {
	var id int64
	var chat string
	var body []byte
	now := s.Now().Unix()
	e := s.Store.DB.QueryRow(`SELECT o.id,o.chat,o.body FROM outbox o WHERE o.due<=? AND NOT EXISTS(SELECT 1 FROM outbox earlier WHERE earlier.chat=o.chat AND earlier.id<o.id) ORDER BY o.id LIMIT 1`, now).Scan(&id, &chat, &body)
	if e == sql.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var m Message
	if e = json.Unmarshal(body, &m); e != nil {
		return false, e
	}
	var previous int64
	e = s.Store.DB.QueryRow("SELECT message FROM cards WHERE chat=?", chat).Scan(&previous)
	if e != nil && e != sql.ErrNoRows {
		return false, e
	}
	messageID, sendErr := sender.Send(ctx, chat, m, previous)
	if sendErr != nil {
		delay, permanent := policy(sendErr)
		if permanent {
			_, e = s.Store.DB.Exec("DELETE FROM outbox WHERE id=?", id)
		} else {
			_, e = s.Store.DB.Exec("UPDATE outbox SET due=? WHERE id=?", now+max(1, int64(delay.Seconds())), id)
		}
		return true, e
	}
	e = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if m.Card {
			if _, e := tx.Exec("INSERT INTO cards VALUES(?,?) ON CONFLICT(chat) DO UPDATE SET message=excluded.message", chat, messageID); e != nil {
				return e
			}
		}
		_, e := tx.Exec("DELETE FROM outbox WHERE id=?", id)
		return e
	})
	return true, e
}
