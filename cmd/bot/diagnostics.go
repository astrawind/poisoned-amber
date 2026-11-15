package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"poisoned-amber/internal/adapters/telegram"
	"poisoned-amber/internal/app"
	"time"
)

// Diagnostics deliberately never accept message text, callback data or secrets.
type diagnostics struct {
	logger *log.Logger
	debug  bool
}

func (d diagnostics) failure(operation string, err error) {
	var api *telegram.APIError
	if errors.As(err, &api) {
		d.logger.Printf("operation=%s status=error telegram_code=%d retry_after=%d", operation, api.Code, api.Retry)
		return
	}
	d.logger.Printf("operation=%s status=error category=transport_or_internal", operation)
}

func (d diagnostics) handled(ctx context.Context, db *sql.DB, in app.Input) {
	if !d.debug {
		return
	}
	kind := "message"
	if in.Callback != "" {
		kind = "callback"
	}
	var phase string
	var phaseID, encounter int
	err := db.QueryRowContext(ctx, `SELECT json_extract(r.body,'$.Phase'),json_extract(r.body,'$.PhaseID'),json_extract(r.body,'$.Encounter') FROM users u JOIN rooms r ON r.id=u.room WHERE u.id=?`, in.User).Scan(&phase, &phaseID, &encounter)
	if err == sql.ErrNoRows {
		d.logger.Printf("operation=update status=handled offset=%d kind=%s room=none", in.Offset, kind)
	} else if err != nil {
		d.failure("debug_state", err)
	} else {
		d.logger.Printf("operation=update status=handled offset=%d kind=%s phase=%s phase_id=%d encounter=%d", in.Offset, kind, phase, phaseID, encounter+1)
	}
}

type observedSender struct {
	sender app.Sender
	d      diagnostics
}

func (s observedSender) Send(ctx context.Context, chat string, m app.Message, previous int64) (int64, error) {
	start := time.Now()
	id, err := s.sender.Send(ctx, chat, m, previous)
	if err != nil {
		s.d.failure("send", err)
	} else if s.d.debug {
		s.d.logger.Printf("operation=send status=ok card=%t elapsed_ms=%d", m.Card, time.Since(start).Milliseconds())
	}
	return id, err
}
