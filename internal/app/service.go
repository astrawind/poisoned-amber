package app

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	mathrand "math/rand"
	db "poisoned-amber/internal/adapters/sqlite"
	"poisoned-amber/internal/domain"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Button struct {
	Text  string `json:"text"`
	Data  string `json:"callback_data"`
	Style string `json:"style,omitempty"`
}
type Message struct {
	Text    string
	Buttons [][]Button
	Card    bool
	Fresh   bool
	HTML    bool
}
type Input struct {
	ID, User, Name, Text, Callback string
	Offset                         int64
}
type Service struct {
	Store   *db.Store
	Version string
	Now     func() time.Time
	RNG     domain.RNG
}

func New(s *db.Store, v string) *Service {
	return &Service{s, v, time.Now, mathrand.New(mathrand.NewSource(time.Now().UnixNano()))}
}
func token() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func password(salt, pw string) string {
	b, err := pbkdf2.Key(sha256.New, pw, []byte(salt), 150000, 32)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func enqueue(tx *sql.Tx, user string, m Message) error {
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO outbox(chat,body) VALUES(?,?)", user, b)
	return e
}
func (s *Service) Handle(ctx context.Context, in Input) error {
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		res, e := tx.Exec("INSERT OR IGNORE INTO commands VALUES(?)", in.ID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		if in.Offset > 0 {
			if _, e = tx.Exec("INSERT INTO metadata VALUES('offset',?) ON CONFLICT(key) DO UPDATE SET value=max(value,excluded.value)", in.Offset); e != nil {
				return e
			}
		}
		if _, e = tx.Exec("INSERT OR IGNORE INTO users(id) VALUES(?)", in.User); e != nil {
			return e
		}
		var adult, fail int
		var rid string
		var blocked int64
		if e = tx.QueryRow("SELECT adult,room,failures,blocked_until FROM users WHERE id=?", in.User).Scan(&adult, &rid, &fail, &blocked); e != nil {
			return e
		}
		if len([]rune(in.Name)) > 40 {
			in.Name = string([]rune(in.Name)[:40])
		}
		now := s.Now().Unix()
		text := strings.TrimSpace(in.Text)
		reply := func(t string) error { return enqueue(tx, in.User, Message{Text: t}) }
		if text == "/adult" {
			_, e = tx.Exec("UPDATE users SET adult=1 WHERE id=?", in.User)
			if e != nil {
				return e
			}
			return reply("✅ 18+ подтверждено.\n\n" + welcomeText)
		}
		if adult == 0 {
			return reply(welcomeText + "\n\n🔞 Игра для совершеннолетних. Сначала подтвердите, что вам исполнилось 18: /adult")
		}
		if in.Callback != "" {
			var user, room, action string
			var phase int
			e = tx.QueryRow("SELECT user,room,phase,action FROM buttons WHERE id=?", in.Callback).Scan(&user, &room, &phase, &action)
			if e != nil || user != in.User || room != rid {
				return reply("Кнопка устарела. /sync")
			}
			r, e := db.Room(tx, rid)
			if e != nil {
				return e
			}
			if r.PhaseID != phase {
				return reply("Фаза уже изменилась. /sync")
			}
			res, e := tx.Exec("INSERT OR IGNORE INTO commands VALUES(?)", "button:"+in.Callback)
			if e != nil {
				return e
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				return nil
			}
			text = action
		}
		parts := strings.Fields(text)
		if len(parts) == 0 {
			return reply("/sync — состояние; /help — команды")
		}
		cmd := strings.Split(parts[0], "@")[0]
		if cmd == "/help" || cmd == "/start" {
			if e := reply(helpText); e != nil {
				return e
			}
			if rid == "" {
				return nil
			}
			cmd = "/sync"
		}
		if rid != "" {
			r, e := db.Room(tx, rid)
			if e != nil {
				return e
			}
			if r.Phase == "ended" || r.Phase == "expired" {
				if cmd == "/create" || cmd == "/join" {
					rid = ""
					if _, e = tx.Exec("UPDATE users SET room='' WHERE id=?", in.User); e != nil {
						return e
					}
				}
			}
		}
		if cmd == "/create" || cmd == "/join" || cmd == "/return" {
			if rid != "" {
				return reply("У вас уже есть поход. /sync или /leave")
			}
			if now < blocked {
				return reply("Слишком много попыток. Попробуйте позже.")
			}
			if cmd == "/create" {
				if len(parts) != 2 || len(parts[1]) < 4 || len(parts[1]) > 128 {
					return reply("Используйте /create пароль (4–128 символов, без пробелов)")
				}
				c, e := db.Content(tx, s.Version)
				if e != nil {
					return e
				}
				r := &domain.Room{ID: token(), Code: strings.ToUpper(token()[:8]), Leader: in.User, ContentVersion: c.Version, Phase: "lobby", PhaseID: 1, LastAction: now}
				r.Password = password(r.ID, parts[1])
				r.Players = []domain.Fighter{{ID: in.User, Name: in.Name}}
				if e = db.Save(tx, r, c); e != nil {
					return e
				}
				if _, e = tx.Exec("UPDATE users SET room=? WHERE id=?", r.ID, in.User); e != nil {
					return e
				}
				return s.broadcast(tx, r, c)
			}
			if len(parts) < 2 {
				return reply("/join КОД пароль или /return КОД")
			}
			var id string
			e = tx.QueryRow("SELECT id FROM rooms WHERE code=?", strings.ToUpper(parts[1])).Scan(&id)
			var r *domain.Room
			if e == nil {
				r, e = db.Room(tx, id)
			}
			valid := e == nil && r.Phase != "ended" && r.Phase != "expired"
			if valid {
				content, err := db.Content(tx, r.ContentVersion)
				if err != nil {
					return err
				}
				valid = now-r.LastAction < int64(content.Balance.ExpirySeconds)
			}
			if valid && cmd == "/return" {
				valid = r.Player(in.User) != nil
			} else if valid {
				valid = len(parts) == 3 && subtle.ConstantTimeCompare([]byte(password(r.ID, parts[2])), []byte(r.Password)) == 1
			}
			if !valid {
				_, e = tx.Exec("UPDATE users SET failures=failures+1,blocked_until=? WHERE id=?", now+int64(min(300, (fail+1)*3)), in.User)
				if e != nil {
					return e
				}
				return reply("Не удалось войти: проверьте код и пароль.")
			}
			p := r.Player(in.User)
			if p == nil {
				if r.Phase != "lobby" || len(r.Players) >= 7 {
					return reply("Вступление закрыто")
				}
				r.Players = append(r.Players, domain.Fighter{ID: in.User, Name: in.Name})
			} else {
				p.Absent = false
			}
			c, e := db.Content(tx, r.ContentVersion)
			if e != nil {
				return e
			}
			r.LastAction = now
			if _, e = tx.Exec("UPDATE users SET room=?,failures=0,blocked_until=0 WHERE id=?", r.ID, in.User); e != nil {
				return e
			}
			if e = db.Save(tx, r, c); e != nil {
				return e
			}
			return s.broadcast(tx, r, c)
		}
		if rid == "" {
			return reply("Создайте комнату /create пароль или войдите /join КОД пароль")
		}
		r, e := db.Room(tx, rid)
		if e != nil {
			return e
		}
		c, e := db.Content(tx, r.ContentVersion)
		if e != nil {
			return e
		}
		p := r.Player(in.User)
		if p == nil || p.Absent {
			return reply("Участник недоступен")
		}
		if cmd == "/sync" {
			return s.render(tx, r, c, p)
		}
		if cmd == "/leave" {
			return reply("Вы перестанете получать сообщения; персонаж останется и будет пропускать ходы. Это не сдача команды. Подтвердить: /leave_confirm")
		}
		if r.Phase == "ended" || r.Phase == "expired" {
			return s.render(tx, r, c, p)
		}
		oldPhase := r.PhaseID
		r.Timeout(c, s.RNG, now)
		if oldPhase != r.PhaseID {
			if r.Phase == "rewards" {
				r.Offers(c, s.RNG)
			}
			if e = db.Save(tx, r, c); e != nil {
				return e
			}
			return s.broadcast(tx, r, c)
		}
		if r.Phase == "challenge" && r.Challenge.Kind == "anagram" && !strings.HasPrefix(text, "/") {
			parts = append([]string{"/answer"}, strings.Fields(text)...)
			cmd = "/answer"
		}
		before := r.Phase
		phase := r.PhaseID
		err := domain.ErrInvalid
		switch cmd {
		case "/leave_confirm":
			leavingID := p.ID
			p.Absent = true
			p.Confirmed = true
			if r.Phase == "lobby" {
				for i, a := range r.Players {
					if a.ID == p.ID {
						r.Players = slices.Delete(r.Players, i, i+1)
						break
					}
				}
			}
			if r.Leader == leavingID {
				for _, a := range r.Players {
					if !a.Absent {
						r.Leader = a.ID
						break
					}
				}
			}
			_, e = tx.Exec("UPDATE users SET room='' WHERE id=?", leavingID)
			if e != nil {
				return e
			}
			if r.Phase == "combat" {
				p.Acted = true
				r.AfterAction(c, s.RNG, now)
			}
			err = nil
		case "/class":
			if r.Phase == "lobby" && len(parts) == 2 && c.Class(parts[1]).ID != "" {
				p.Class = parts[1]
				p.Ready = false
				err = nil
			}
		case "/background":
			if r.Phase == "lobby" && len(parts) == 2 && c.Background(parts[1]).ID != "" {
				p.Background = parts[1]
				p.Ready = false
				err = nil
			}
		case "/ready":
			if r.Phase == "lobby" && p.Class != "" && p.Background != "" {
				p.Ready = !p.Ready
				err = nil
			}
		case "/begin":
			if r.Leader == p.ID {
				err = r.Start(c, now)
			}
		case "/act":
			if len(parts) >= 2 {
				target := ""
				if len(parts) == 3 {
					target = parts[2]
				}
				err = r.Act(c, s.RNG, p.ID, parts[1], target, now)
			}
		case "/continue":
			if r.Leader == p.ID {
				if (r.Phase == "preparation" || r.Phase == "transition") && !r.MemoryReady() {
					return reply("Ожидаем подтверждения памяти всех живых участников. Нажмите «Подтвердить память»; по истечении срока сохранится прежний набор.")
				}
				err = r.Continue(c, s.RNG, now)
			}
		case "/fight":
			if r.Leader == p.ID && (r.Phase == "challenge" || r.Phase == "preparation" && !c.Route[r.Encounter].Final && r.MemoryReady()) {
				r.BeginCombat(c, now)
				err = nil
			}
		case "/answer":
			if len(parts) > 1 {
				err = r.ChallengeInput(c, p.ID, strings.Join(parts[1:], " "), -1, now)
			}
		case "/cell":
			if len(parts) == 2 {
				v, x := strconv.Atoi(parts[1])
				if x == nil {
					err = r.ChallengeInput(c, p.ID, "", v, now)
				}
			}
		case "/vote":
			if len(parts) == 2 && (parts[1] == "yes" || parts[1] == "no") {
				err = r.CastVote(c, p.ID, parts[1] == "yes", now)
			}
		case "/memory", "/toggle":
			ids := slices.Clone(parts[1:])
			if len(ids) == 1 && ids[0] == "empty" {
				ids = nil
			}
			if cmd == "/toggle" && len(parts) == 2 {
				ids = slices.Clone(p.Draft)
				i := slices.Index(ids, parts[1])
				if i >= 0 {
					ids = slices.Delete(ids, i, i+1)
				} else {
					ids = append(ids, parts[1])
				}
			}
			copyP := *p
			err = r.SetMemory(c, &copyP, ids)
			if err == nil {
				p.Draft = ids
				p.Confirmed = false
			}
		case "/confirm":
			err = r.SetMemory(c, p, p.Draft)
			if err == nil {
				p.Confirmed = true
				if r.Phase == "rewards" && r.MemoryReady() {
					r.FinishMemory()
					r.SetPhase("transition", c.Balance.PreparationSeconds, now)
				}
			}
		case "/pause":
			if r.Leader == p.ID && (r.Phase == "preparation" || r.Phase == "transition" || r.Phase == "rewards") {
				r.PausedPhase = r.Phase
				r.Remaining = max(0, r.Deadline-now)
				r.SetPhase("paused", 0, now)
				err = nil
			}
		case "/resume":
			if r.Leader == p.ID && r.Phase == "paused" {
				r.SetPhase(r.PausedPhase, int(r.Remaining), now)
				err = nil
			}
		case "/story":
			if r.Phase == "preparation" || r.Phase == "transition" {
				bg := c.Background(p.Background)
				p.Flags = append(p.Flags, bg.Flag)
				r.Log = append(r.Log, p.Name+": "+bg.Reply)
				err = nil
			}
		}
		if err != nil {
			return reply("Действие недоступно. Проверьте фазу и цель: /sync")
		}
		r.LastAction = now
		if r.Phase == "rewards" && before != "paused" && (before != "rewards" || phase != r.PhaseID) {
			r.Offers(c, s.RNG)
		}
		if e = db.Save(tx, r, c); e != nil {
			return e
		}
		if cmd == "/toggle" || cmd == "/memory" {
			return s.render(tx, r, c, p)
		}
		return s.broadcast(tx, r, c)
	})
}
func (s *Service) Tick(ctx context.Context) error {
	now := s.Now().Unix()
	rows, e := s.Store.DB.Query("SELECT id FROM rooms WHERE (deadline>0 AND deadline<=?) OR (expiry>0 AND expiry<=?)", now, now)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.Store.Tx(ctx, func(tx *sql.Tx) error {
			r, e := db.Room(tx, id)
			if e != nil {
				return e
			}
			c, e := db.Content(tx, r.ContentVersion)
			if e != nil {
				return e
			}
			phase := r.PhaseID
			r.Timeout(c, s.RNG, now)
			if r.PhaseID == phase {
				return nil
			}
			if r.Phase == "rewards" {
				r.Offers(c, s.RNG)
			}
			if e = db.Save(tx, r, c); e != nil {
				return e
			}
			return s.broadcast(tx, r, c)
		}); e != nil {
			return e
		}
	}
	return nil
}
