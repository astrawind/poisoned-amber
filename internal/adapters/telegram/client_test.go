package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"poisoned-amber/internal/app"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRetryAndEditFallback(t *testing.T) {
	calls := 0
	c := New("test-secret")
	c.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"ok":true,"result":{"message_id":42}}`
		switch calls {
		case 1:
			body = `{"ok":false,"error_code":429,"parameters":{"retry_after":17}}`
		case 2:
			body = `{"ok":false,"error_code":400}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	_, e := c.Send(context.Background(), "1", app.Message{Text: "hello", Card: true}, 1)
	var api *APIError
	if !errors.As(e, &api) || api.Retry != 17 {
		t.Fatal(e)
	}
	id, e := c.Send(context.Background(), "1", app.Message{Text: "hello", Card: true}, 1)
	if e != nil || id != 42 || calls != 3 {
		t.Fatal(id, e, calls)
	}
}
