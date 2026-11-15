package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"poisoned-amber/internal/app"
	"strconv"
	"time"
)

type APIError struct{ Code, Retry int }

func (e *APIError) Error() string { return fmt.Sprintf("telegram status %d", e.Code) }

type Client struct {
	Token string
	HTTP  *http.Client
	Base  string
}

func New(token string) *Client {
	return &Client{token, &http.Client{Timeout: 40 * time.Second}, "https://api.telegram.org"}
}
func (c *Client) call(ctx context.Context, method string, body any, result any) error {
	b, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.Base+"/bot"+c.Token+"/"+method, bytes.NewReader(b))
	if e != nil {
		return errors.New("invalid Telegram endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return errors.New("telegram transport unavailable")
	}
	defer resp.Body.Close()
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Code       int             `json:"error_code"`
		Parameters struct {
			Retry int `json:"retry_after"`
		} `json:"parameters"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&envelope); e != nil {
		return errors.New("invalid telegram response")
	}
	if !envelope.OK {
		return &APIError{envelope.Code, envelope.Parameters.Retry}
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

type user struct {
	ID   int64  `json:"id"`
	Name string `json:"first_name"`
}
type message struct {
	ID   int64  `json:"message_id"`
	From user   `json:"from"`
	Text string `json:"text"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}
type update struct {
	ID       int64    `json:"update_id"`
	Message  *message `json:"message"`
	Callback *struct {
		ID      string   `json:"id"`
		From    user     `json:"from"`
		Data    string   `json:"data"`
		Message *message `json:"message"`
	} `json:"callback_query"`
}

func (c *Client) Poll(ctx context.Context, offset int64) ([]app.Input, error) {
	var us []update
	if e := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &us); e != nil {
		return nil, e
	}
	var out []app.Input
	for _, u := range us {
		in := app.Input{ID: fmt.Sprintf("update:%d", u.ID), Offset: u.ID + 1}
		if u.Message != nil && u.Message.Chat.Type == "private" && u.Message.Chat.ID == u.Message.From.ID {
			in.User = strconv.FormatInt(u.Message.From.ID, 10)
			in.Name = u.Message.From.Name
			in.Text = u.Message.Text
		}
		if u.Callback != nil && u.Callback.Message != nil && u.Callback.Message.Chat.Type == "private" && u.Callback.Message.Chat.ID == u.Callback.From.ID {
			in.User = strconv.FormatInt(u.Callback.From.ID, 10)
			in.Name = u.Callback.From.Name
			in.Callback = u.Callback.Data
			_ = c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": u.Callback.ID}, nil)
		}
		out = append(out, in)
	}
	return out, nil
}
func (c *Client) Send(ctx context.Context, chat string, m app.Message, previous int64) (int64, error) {
	body := map[string]any{"chat_id": chat, "text": m.Text, "reply_markup": map[string]any{"inline_keyboard": m.Buttons}}
	method := "sendMessage"
	if m.Card && previous > 0 {
		body["message_id"] = previous
		method = "editMessageText"
	}
	var result struct {
		ID int64 `json:"message_id"`
	}
	e := c.call(ctx, method, body, &result)
	var api *APIError
	if errors.As(e, &api) && api.Code == 400 && method == "editMessageText" {
		delete(body, "message_id")
		e = c.call(ctx, "sendMessage", body, &result)
	}
	return result.ID, e
}
