package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	db "poisoned-amber/internal/adapters/sqlite"
	"poisoned-amber/internal/adapters/telegram"
	"poisoned-amber/internal/app"
	"poisoned-amber/internal/domain"
	"strconv"
	"sync"
	"syscall"
	"time"
)

func env(k, v string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return v
}
func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
func run() error {
	debug, e := strconv.ParseBool(env("DEBUG", "false"))
	if e != nil {
		return fmt.Errorf("DEBUG must be true or false")
	}
	diag := diagnostics{logger: log.Default(), debug: debug}
	f, e := os.Open(env("CONTENT_FILE", "content/test.json"))
	if e != nil {
		return e
	}
	c, e := domain.ReadContent(f)
	f.Close()
	if e != nil {
		return e
	}
	s, e := db.Open(env("DB_PATH", "amber.db"))
	if e != nil {
		return e
	}
	defer s.Close()
	if e = s.Import(c); e != nil {
		return e
	}
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return fmt.Errorf("BOT_TOKEN is required; offline check: go test ./...")
	}
	svc := app.New(s, c.Version)
	client := telegram.New(token)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	workers.Add(2)
	errch := make(chan error, 2)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				errch <- nil
				return
			case <-ticker.C:
				if e := svc.Tick(ctx); e != nil {
					diag.failure("scheduler", e)
					errch <- e
					return
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				errch <- nil
				return
			case <-ticker.C:
				_, e := svc.Deliver(ctx, observedSender{sender: client, d: diag}, func(e error) (time.Duration, bool) {
					var api *telegram.APIError
					if errors.As(e, &api) {
						if api.Code == 403 || api.Code == 400 {
							return 0, true
						}
						if api.Code == 429 {
							return time.Duration(max(1, api.Retry)) * time.Second, false
						}
					}
					return 5 * time.Second, false
				})
				if e != nil {
					diag.failure("outbox", e)
					errch <- e
					return
				}
			}
		}
	}()
	log.Printf("Poisoned Amber started debug=%t", debug)
	for ctx.Err() == nil {
		select {
		case e := <-errch:
			cancel()
			return e
		default:
		}
		var offset int64
		e = s.DB.QueryRow("SELECT value FROM metadata WHERE key='offset'").Scan(&offset)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		inputs, e := client.Poll(ctx, offset)
		if e != nil {
			if ctx.Err() == nil {
				diag.failure("poll", e)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
				continue
			}
		}
		for _, in := range inputs {
			if in.User == "" {
				_, e = s.DB.Exec("INSERT INTO metadata VALUES('offset',?) ON CONFLICT(key) DO UPDATE SET value=max(value,excluded.value)", in.Offset)
			} else {
				e = svc.Handle(ctx, in)
			}
			if e != nil {
				diag.failure("update", e)
				return e
			}
			if in.User != "" {
				diag.handled(ctx, s.DB, in)
			}
		}
	}
	return nil
}
