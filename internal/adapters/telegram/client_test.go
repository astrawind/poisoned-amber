package telegram

import (
	"context"
	"encoding/json"
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

func TestHistorySendsNewCardWhileSyncEditsLatest(t *testing.T) {
	var methods []string
	c := New("test-secret")
	c.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		methods = append(methods, r.URL.Path)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["parse_mode"] != "HTML" {
			t.Fatal("missing formatting")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)), Header: make(http.Header)}, nil
	})}
	for _, fresh := range []bool{true, false} {
		if _, err := c.Send(context.Background(), "1", app.Message{Text: "<b>Бой</b>", Card: true, Fresh: fresh, HTML: true}, 41); err != nil {
			t.Fatal(err)
		}
	}
	if len(methods) != 2 || !strings.HasSuffix(methods[0], "/sendMessage") || !strings.HasSuffix(methods[1], "/editMessageText") {
		t.Fatal(methods)
	}
}

func TestButtonStylesSerializeOnlyWhenSpecified(t *testing.T) {
	c := New("test-secret")
	c.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Markup struct {
				Buttons [][]map[string]any `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		buttons := body.Markup.Buttons
		if buttons[0][0]["style"] != "primary" || buttons[1][0]["style"] != "success" {
			t.Fatal(buttons)
		}
		if _, exists := buttons[2][0]["style"]; exists {
			t.Fatal("default control received style")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)), Header: make(http.Header)}, nil
	})}
	_, err := c.Send(context.Background(), "1", app.Message{Text: "Память", Buttons: [][]app.Button{
		{{Text: "Способность", Data: "a", Style: "primary"}},
		{{Text: "Подтвердить", Data: "b", Style: "success"}},
		{{Text: "Пауза", Data: "c"}},
	}}, 0)
	if err != nil {
		t.Fatal(err)
	}
}
