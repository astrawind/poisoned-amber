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
	"fmt"
	mathrand "math/rand"
	db "poisoned-amber/internal/adapters/sqlite"
	"poisoned-amber/internal/domain"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}
type Message struct {
	Text    string
	Buttons [][]Button
	Card    bool
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
			return reply("18+ подтверждено. Создать: /create пароль. Войти: /join КОД пароль. Пароль отправляйте только в личный чат.")
		}
		if adult == 0 {
			return reply("Poisoned Amber — игра для совершеннолетних. Подтвердите, что вам исполнилось 18: /adult")
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
			if rid == "" {
				return reply("/create пароль · /join КОД пароль · /return КОД\nПосле входа выберите класс, предысторию и готовность.")
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
				err = r.Continue(c, s.RNG, now)
			}
		case "/fight":
			if r.Leader == p.ID && (r.Phase == "challenge" || r.Phase == "preparation" && !c.Route[r.Encounter].Final) {
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
			if r.Phase == "vote" && p.HP > 0 && len(parts) == 2 && (parts[1] == "yes" || parts[1] == "no") {
				if _, ok := r.Votes[p.ID]; !ok {
					r.Votes[p.ID] = parts[1] == "yes"
					err = nil
					all := true
					for _, a := range r.Players {
						if a.HP > 0 && !a.Absent {
							if _, ok := r.Votes[a.ID]; !ok {
								all = false
							}
						}
					}
					if all {
						r.FinishVote(c, now)
					}
				}
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
			}
		case "/confirm":
			err = r.SetMemory(c, p, p.Draft)
			if err == nil && r.Phase == "rewards" {
				p.Confirmed = true
				all := true
				for _, a := range r.Players {
					if !a.Confirmed {
						all = false
					}
				}
				if all {
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
func (s *Service) broadcast(tx *sql.Tx, r *domain.Room, c *domain.Content) error {
	for i := range r.Players {
		if !r.Players[i].Absent {
			if e := s.render(tx, r, c, &r.Players[i]); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Service) render(tx *sql.Tx, r *domain.Room, c *domain.Content, p *domain.Fighter) error {
	m := Message{Card: true}
	m.Text = fmt.Sprintf("Poisoned Amber · %s\nФаза: %s · круг %d · встреча %d/9\n", r.Code, phaseName(r.Phase), r.Round, r.Encounter+1)
	add := func(label, action string) error {
		id := token()
		if _, e := tx.Exec("INSERT INTO buttons VALUES(?,?,?,?,?)", id, p.ID, r.ID, r.PhaseID, action); e != nil {
			return e
		}
		m.Buttons = append(m.Buttons, []Button{{label, id}})
		return nil
	}
	var buildErr error
	button := func(label, action string) {
		if buildErr == nil {
			buildErr = add(label, action)
		}
	}
	for _, a := range r.Players {
		m.Text += fmt.Sprintf("%s [%s] HP %d/%d +%d · готов %t\n", a.Name, a.Class, a.HP, a.Stats(c).HP, a.Temp, a.Ready)
	}
	if r.Phase == "lobby" {
		for _, cl := range c.Classes {
			button(cl.Name, "/class "+cl.ID)
		}
		for _, bg := range c.Backgrounds {
			button(bg.Name, "/background "+bg.ID)
		}
		button("Готовность", "/ready")
		if r.Leader == p.ID {
			button("Начать поход", "/begin")
		}
	}
	m.Text += "\nВаша память:\n"
	for _, id := range p.Memory {
		v := c.Memory(id)
		m.Text += fmt.Sprintf("%s — %s (%s), бонус HP +%d, сила +%d, точность +%d; %v\n", id, v.Name, v.Rarity, v.Bonus.HP, v.Bonus.Attack, v.Bonus.Accuracy, effectText(v.Effects))
	}
	if r.Phase == "combat" {
		for _, e := range r.Enemies {
			m.Text += fmt.Sprintf("Враг %s: %s HP %d\n", e.ID, c.Enemy(e.Def).Name, e.HP)
		}
		if !p.Acted && p.HP > 0 {
			for _, id := range p.Memory {
				v := c.Memory(id)
				if v.Type != "active" {
					continue
				}
				switch v.Target {
				case "enemy":
					for _, e := range r.Enemies {
						if e.HP > 0 {
							button(v.Name+" → "+e.ID, "/act "+id+" "+e.ID)
						}
					}
				case "ally":
					for _, a := range r.Players {
						if a.HP > 0 {
							button(v.Name+" → "+a.Name, "/act "+id+" "+a.ID)
						}
					}
				default:
					button(v.Name, "/act "+id)
				}
			}
			button("Пропустить", "/act skip")
		}
	}
	if r.Phase == "rewards" || r.Phase == "preparation" || r.Phase == "transition" {
		preview := *p
		preview.Memory = p.Draft
		m.Text += fmt.Sprintf("\nВыбрано: %s\nПосле подтверждения: HP %d, сила %d, точность %d\n", strings.Join(p.Draft, ", "), preview.Stats(c).HP, preview.Stats(c).Attack, preview.Stats(c).Accuracy)
		if !p.Confirmed || r.Phase != "rewards" {
			for _, id := range append(slices.Clone(p.Memory), p.Offer...) {
				label := "Добавить "
				if slices.Contains(p.Draft, id) {
					label = "Убрать "
				}
				button(label+c.Memory(id).Name+" "+c.Memory(id).Rarity, "/toggle "+id)
			}
			if r.Phase != "rewards" {
				button("Подтвердить память", "/confirm")
			}
		}
		m.Text += fmt.Sprintf("\nСлотов: %d. Выбор и порядок: /memory ID1 ID2 …; забыть всё: /memory empty. Затем /confirm\n", p.Slots)
		for _, id := range p.Offer {
			v := c.Memory(id)
			m.Text += fmt.Sprintf("Предложение %s: %s, %s, бонус %+v, эффекты %v\n", id, v.Name, v.Rarity, v.Bonus, effectText(v.Effects))
		}
		if r.Phase == "rewards" && !p.Confirmed {
			button("Подтвердить выбор", "/confirm")
		}
		button("Реплика предыстории", "/story")
		if r.Leader == p.ID {
			button("Продолжить", "/continue")
			button("Пауза", "/pause")
		}
	}
	if r.Phase == "paused" && r.Leader == p.ID {
		button("Возобновить", "/resume")
	}
	if r.Phase == "challenge" {
		ch := r.Challenge
		m.Text += "\nИспытание: " + phaseName(ch.Kind) + "\n"
		switch ch.Kind {
		case "crocodile", "three_p":
			m.Text += "Исполнитель: " + r.Player(ch.Performer).Name + ". Объясните жестами или словами на П согласно типу.\n"
			if p.ID == ch.Performer {
				m.Text += "Секрет: " + ch.Words[0] + "\n"
				button("Угадали", "/answer solved")
			}
		case "anagram":
			m.Text += "Буквы: " + ch.Scramble + ". Ответ: /answer слово\n"
		case "codenames":
			m.Text += "Капитан: " + r.Player(ch.Captain).Name + "; угадывает: " + r.Player(ch.Guesser).Name + "\n"
			for i, w := range ch.Words {
				m.Text += w
				if slices.Contains(ch.Open, i) {
					m.Text += " [открыто]"
				}
				if p.ID == ch.Captain {
					if i == ch.Black {
						m.Text += " [чёрное]"
					} else {
						m.Text += " [синее]"
					}
				}
				m.Text += "\n"
				if p.ID == ch.Guesser {
					button(w, fmt.Sprintf("/cell %d", i))
				}
			}
		}
		if r.Leader == p.ID {
			button("Сразу бой", "/fight")
		}
	}
	if r.Phase == "vote" {
		m.Text += "Присоединиться к боссу? Молчание считается отказом. При равенстве решает живой глава, иначе первый живой.\n"
		button("Присоединиться", "/vote yes")
		button("Отказаться", "/vote no")
	}
	if r.Deadline > 0 {
		m.Text += fmt.Sprintf("Срок: %s UTC\n", time.Unix(r.Deadline, 0).UTC().Format("15:04:05"))
	}
	m.Text += "\n" + strings.Join(r.Log, "\n")
	if r.Phase == "ended" {
		m.Text += "\nИсход: " + r.Outcome
		for _, a := range r.Players {
			for _, f := range a.Flags {
				if f == "tunnels" {
					m.Text += "\nЗнание штолен сохранилось в рассказах горняков."
				}
				if f == "witness" {
					m.Text += "\nСтранник понесёт историю за пределы гор."
				}
			}
		}
	}
	if buildErr != nil {
		return buildErr
	}
	return enqueue(tx, p.ID, m)
}

func phaseName(s string) string {
	names := map[string]string{"lobby": "лобби", "combat": "бой", "preparation": "подготовка у бочки", "rewards": "награды", "transition": "переход", "paused": "пауза", "challenge": "испытание", "vote": "голосование", "ended": "поход завершён", "expired": "комната истекла", "joined": "сторона босса принята", "slain": "босс повержен", "defeat": "поражение", "crocodile": "Крокодил", "three_p": "Три-П", "anagram": "Анаграмма", "codenames": "Три слова"}
	if n, ok := names[s]; ok {
		return n
	}
	return s
}
func effectText(es []domain.Effect) string {
	var out []string
	for _, e := range es {
		v := ""
		switch e.Kind {
		case "damage", "counter":
			v = fmt.Sprintf("урон %d%% силы", e.Power)
		case "temp_hp":
			v = fmt.Sprintf("временные HP +%d", e.Power)
		case "poison":
			v = fmt.Sprintf("яд %d, тиков %d", e.Power, e.Duration)
		case "skip":
			v = fmt.Sprintf("пропуск действия: %d%%", e.Chance)
		case "heal":
			v = fmt.Sprintf("лечение %d%% максимума, одноразовое", e.Power)
		case "boost":
			v = fmt.Sprintf("сила +%d на %d фазы", e.Power, e.Duration)
		case "taunt":
			v = fmt.Sprintf("провокация на %d фазы", e.Duration)
		case "intercept":
			v = "перехват атаки союзника"
		}
		out = append(out, v)
	}
	return strings.Join(out, "; ")
}
