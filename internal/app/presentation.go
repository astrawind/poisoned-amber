package app

import (
	"database/sql"
	"fmt"
	"html"
	"poisoned-amber/internal/domain"
	"slices"
	"strings"
	"time"
)

const welcomeText = "🍺 Poisoned Amber\nКооперативное приключение для 2–7 друзей за одним столом. Дварфское пиво перестало поступать в столицу, рабочие негодуют, а поселение не отвечает на письма. Выясните, что случилось, и восстановите поставки.\n\n1. Глава создаёт комнату: /create пароль\n2. Остальные входят: /join КОД пароль\n3. Выберите класс и предысторию, нажмите «Готов», глава начинает поход.\n\n/help — все команды · /sync — текущий экран\nПароль отправляйте только в личный чат боту."
const adultDrinkText = "✅ 18+ подтверждено.\n\n🍻 <b>ПОДГОТОВЬТЕ КРУЖКИ</b>\nДля полного погружения подготовьте своё пиво или другой напиток — чай, сок и вода тоже подходят.\n\nКогда принимаете бочку или применяете лечебное воспоминание, сделайте глоток своего напитка. Бот назовёт игрока, чья очередь поднять кружку.\n\n<i>Пивоварня учитывает подвиги, а не объём кружки.</i>"
const helpText = welcomeText + "\n\n📋 Команды\n/adult — подтвердить 18+\n/return КОД — вернуться в свой поход\n/leave — выйти (сначала появится предупреждение)\n/pause, /resume — пауза между встречами (глава)\n\nИграйте кнопками под последним экраном. Способности отмечены цветом редкости: ⚪ обычная · 🔵 редкая · 🟣 эпическая · 🟠 легендарная.\n/answer слово — ответ в анаграмме\n/memory ID1 ID2 — выбрать память по ID\n/memory empty — забыть всё; /confirm — подтвердить."

func (s *Service) broadcast(tx *sql.Tx, r *domain.Room, c *domain.Content) error {
	for i := range r.Players {
		if !r.Players[i].Absent {
			if e := s.renderView(tx, r, c, &r.Players[i], true); e != nil {
				return e
			}
		}
	}
	return nil
}

// Enqueued only for an accepted action, never from state rendering or /sync.
// Not a Card: delivery must not replace the current controls with a toast.
func broadcastDrinkBanner(tx *sql.Tx, r *domain.Room, actor *domain.Fighter, reason string) error {
	name := actor.Name
	if strings.TrimSpace(name) == "" {
		name = "Игрок"
	}
	m := Message{HTML: true, Fresh: true, Text: "━━━━━━━━━━━━━━\n🍻 <b>КРУЖКУ ВВЕРХ!</b>\n━━━━━━━━━━━━━━\n\n<b>" + esc(name) + ", сделай глоток своего напитка!</b>\n" + esc(reason) + "\n\n<i>Пивоварня одобряет. Состав кружки остаётся твоим делом.</i>"}
	for _, p := range r.Players {
		if !p.Absent {
			if err := enqueue(tx, p.ID, m); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) render(tx *sql.Tx, r *domain.Room, c *domain.Content, p *domain.Fighter) error {
	return s.renderView(tx, r, c, p, false)
}
func esc(s string) string {
	return html.EscapeString(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}
func (s *Service) renderView(tx *sql.Tx, r *domain.Room, c *domain.Content, p *domain.Fighter, fresh bool) error {
	m := Message{Card: true, Fresh: fresh, HTML: true}
	line := func(v string) { m.Text += v + "\n" }
	heading := func(v string) { line("\n<b>" + esc(v) + "</b>") }
	line("<b>🍺 Poisoned Amber · " + esc(r.Code) + "</b>")
	line("<b>" + esc(phaseName(r.Phase)) + "</b>")
	if r.Phase != "lobby" && r.Encounter < len(c.Route) {
		line(fmt.Sprintf("Встреча %d/%d · %s", r.Encounter+1, len(c.Route), esc(r.CurrentEncounter(c).Name)))
		if c.Schema == 2 {
			line(fmt.Sprintf("🍺 Принято угощений: %d · столько миньонов появится при отказе от союза.", r.Drinks))
		}
	}
	var buildErr error
	button := func(label, action string) {
		if buildErr != nil {
			return
		}
		id := token()
		_, buildErr = tx.Exec("INSERT INTO buttons VALUES(?,?,?,?,?)", id, p.ID, r.ID, r.PhaseID, action)
		if buildErr == nil {
			style := ""
			if strings.HasPrefix(action, "/toggle ") || (strings.HasPrefix(action, "/act ") && action != "/act skip") {
				style = "primary"
			}
			if action == "/confirm" {
				style = "success"
			}
			m.Buttons = append(m.Buttons, []Button{{Text: label, Data: id, Style: style}})
		}
	}
	if r.Phase == "lobby" {
		heading("👥 Команда")
		for _, a := range r.Players {
			status := "⏳ выбирает"
			if a.Ready {
				status = "✅ готов"
			}
			cl, bg := c.Class(a.Class).Name, c.Background(a.Background).Name
			if cl == "" {
				cl = "класс не выбран"
			}
			if bg == "" {
				bg = "предыстория не выбрана"
			}
			line(fmt.Sprintf("%s · %s / %s · %s", esc(a.Name), esc(cl), esc(bg), status))
		}
		heading("1 · Выберите класс")
		for _, cl := range c.Classes {
			label := classIcon(cl.ID) + " " + cl.Name
			if p.Class == cl.ID {
				label = "✓ " + label
			}
			button(label, "/class "+cl.ID)
		}
		heading("2 · Выберите предысторию")
		for _, bg := range c.Backgrounds {
			line("<b>" + esc(bg.Name) + "</b> · " + esc(bg.Race) + ". " + esc(backgroundSummary(bg)))
			label := "📜 " + bg.Name
			if p.Background == bg.ID {
				label = "✓ " + label
			}
			button(label, "/background "+bg.ID)
		}
		label := "✅ Готов"
		if p.Ready {
			label = "↩ Изменить готовность"
		}
		button(label, "/ready")
		if r.Leader == p.ID {
			button("🚪 Начать поход", "/begin")
		}
		line("\nВыберите класс и предысторию, затем подтвердите готовность. /help — команды.")
	} else {
		heading("👥 Команда")
		for _, a := range r.Players {
			status := ""
			if a.HP == 0 {
				status = " · 💀 выбыл"
			} else if a.Absent {
				status = " · 💤 отсутствует"
			} else if r.Phase == "combat" {
				if a.Acted {
					status = " · ✓ сходил"
				} else {
					status = " · ⏳ ходит"
				}
			}
			line(fmt.Sprintf("%s %s · ♥ %d/%d · 🛡 %d%s", classIcon(a.Class), esc(a.Name), a.HP, a.Stats(c).HP, a.Temp, status))
			if effects := timedText(a.Effects); effects != "" {
				line("  " + esc(effects))
			}
		}
	}
	if r.Phase == "barrel" {
		heading("🍺 Угощение от пивоварни")
		line("Каждый решает за себя. Выпить — полностью восстановить HP, даже после выбывания, и добавить одного миньона в финале. Отказаться — сохранить нынешнее здоровье. Решение окончательное; молчание к сроку считается отказом.")
		for _, a := range r.Players {
			choice := "⏳ выбирает"
			if drink, done := r.BarrelChoices[a.ID]; done {
				choice = "отказался"
				if drink {
					choice = "выпил"
				}
			} else if a.Absent {
				choice = "отсутствует — отказ по умолчанию"
			}
			line(esc(a.Name) + " · " + choice)
		}
		if _, done := r.BarrelChoices[p.ID]; !done && !p.Absent {
			button("🍺 Выпить и восстановиться", "/barrel yes")
			button("✋ Отказаться", "/barrel no")
		}
		if r.Leader == p.ID {
			button("⏸ Пауза", "/pause")
		}
	}
	if r.Phase == "combat" {
		if r.FinalStage == "minions" {
			heading("🫧 Пивные миньоны · перед алхимиком лечения не будет")
		}
		heading(fmt.Sprintf("⚔ Бой · круг %d", r.Round))
		for i, e := range r.Enemies {
			line(fmt.Sprintf("%d. %s · ♥ %d · 🛡 %d", i+1, esc(c.Enemy(e.Def).Name), e.HP, e.Temp))
			if effects := timedText(e.Effects); effects != "" {
				line("  " + esc(effects))
			}
		}
		heading("🧠 Ваши способности")
		for _, id := range p.Memory {
			line(memoryDescription(c.Memory(id)))
		}
		if len(p.Memory) == 0 {
			line("Память пуста. Доступен только пропуск хода.")
		}
		if !p.Acted && p.HP > 0 && !p.Absent {
			line("\nВыберите способность и цель:")
			for _, id := range p.Memory {
				v := c.Memory(id)
				if v.Type != "active" {
					continue
				}
				label := memoryLabel(v)
				switch v.Target {
				case "enemy":
					for i, e := range r.Enemies {
						if e.HP > 0 {
							button(fmt.Sprintf("%s → %d. %s", label, i+1, c.Enemy(e.Def).Name), "/act "+id+" "+e.ID)
						}
					}
				case "ally":
					for _, a := range r.Players {
						if a.HP > 0 {
							button(label+" → "+a.Name, "/act "+id+" "+a.ID)
						}
					}
				default:
					button(label, "/act "+id)
				}
			}
			button("⏭ Пропустить ход", "/act skip")
		} else if p.HP > 0 {
			line("\n✓ Ваш ход завершён. Ждём остальных участников.")
		}
	}
	if r.Phase == "rewards" || r.Phase == "preparation" || r.Phase == "transition" {
		heading("🧠 Настройка памяти")
		line("⚪ обычная · 🔵 редкая · 🟣 эпическая · 🟠 легендарная")
		line(fmt.Sprintf("Выбрано %d из %d слотов. Один навык занимает один слот.", len(p.Draft), p.Slots))
		seen := map[string]bool{}
		for _, id := range append(slices.Clone(p.Memory), p.Offer...) {
			if seen[id] {
				continue
			}
			seen[id] = true
			v := c.Memory(id)
			marker := "▫️"
			if slices.Contains(p.Draft, id) {
				marker = "✅"
			}
			source := "Уже в вашей памяти"
			if slices.Contains(p.Offer, id) {
				source = "🆕 Новое предложение"
			}
			line("\n" + marker + " " + memoryDescription(v) + "\n  " + source)
			if r.Phase != "rewards" || !p.Confirmed {
				button(marker+" "+memoryLabel(v), "/toggle "+id)
			}
		}
		preview := *p
		preview.Memory = p.Draft
		stats := preview.Stats(c)
		line(fmt.Sprintf("\nПосле подтверждения: максимум ♥ %d · сила %d · точность %d%%", stats.HP, stats.Attack, stats.Accuracy))
		if p.Confirmed {
			line("✅ Ваша память подтверждена.")
		} else if p.HP > 0 && !p.Absent {
			button("✅ Подтвердить память", "/confirm")
		}
		var waiting []string
		for _, a := range r.Players {
			if a.HP > 0 && !a.Absent && !a.Confirmed {
				waiting = append(waiting, a.Name)
			}
		}
		if len(waiting) > 0 {
			line("⏳ Ждём подтверждения: " + esc(strings.Join(waiting, ", ")))
		}
		line("Нажмите навык, чтобы включить или убрать его, затем подтвердите. Изменения применяются только после подтверждения.")
		if r.Phase != "rewards" && !r.StoryUsed[fmt.Sprintf("%d:%s", r.Encounter, p.ID)] {
			button("💬 Реплика предыстории", "/story")
		}
		if r.Leader == p.ID {
			if r.Phase != "rewards" && r.MemoryReady() {
				button("➡ Продолжить", "/continue")
			}
			button("⏸ Пауза", "/pause")
		}
	}
	if r.Phase == "paused" {
		line("\n⏸ Поход на паузе. Оставшееся время сохранено.")
		if r.Leader == p.ID {
			button("▶ Возобновить", "/resume")
		}
	}
	if r.Phase == "challenge" {
		ch := r.Challenge
		heading("🎭 Испытание · " + phaseName(ch.Kind))
		switch ch.Kind {
		case "crocodile", "three_p":
			line("Исполнитель: <b>" + esc(r.Player(ch.Performer).Name) + "</b>")
			if ch.Kind == "crocodile" {
				line("Объясните слово жестами, без речи. Остальные угадывают вслух.")
			} else {
				line("Объясните слово словами, начинающимися на «П». Остальные угадывают вслух.")
			}
			if p.ID == ch.Performer {
				heading("🔒 Только для вас · слово для объяснения")
				line("<b>«" + esc(ch.Words[0]) + "»</b>")
				line("Не показывайте экран другим. Когда друзья угадают, нажмите кнопку.")
				button("✅ Слово угадали", "/answer solved")
			} else {
				line("Секретное слово видит только исполнитель.")
			}
		case "anagram":
			heading("🔤 Соберите слово")
			line("<b>" + esc(ch.Scramble) + "</b>")
			line("Любой участник может ответить: /answer слово")
		case "codenames":
			line("Капитан: " + esc(r.Player(ch.Captain).Name) + " · угадывает: " + esc(r.Player(ch.Guesser).Name))
			line("Найдите два синих слова и избегайте чёрного. Капитан даёт подсказку вслух; цвета видит только капитан.")
			for i, w := range ch.Words {
				marker := "▫️"
				if p.ID == ch.Captain {
					marker = "🔵"
					if i == ch.Black {
						marker = "⚫"
					}
				}
				suffix := ""
				if slices.Contains(ch.Open, i) {
					suffix = " · открыто"
				}
				line(marker + " <b>" + esc(w) + "</b>" + suffix)
				if p.ID == ch.Guesser && !slices.Contains(ch.Open, i) {
					button("▫️ "+w, fmt.Sprintf("/cell %d", i))
				}
			}
		}
		if r.Leader == p.ID {
			button("⚔ Перейти к бою", "/fight")
		}
	}
	if r.Phase == "vote" {
		heading("⚖ Предложение хозяина тумана")
		if c.Finale != nil {
			line(esc(c.Finale.Speeches[r.Speech]))
			line(fmt.Sprintf("При отказе: %d миньонов, затем алхимик без лечения. Он делает обычную атаку и копирует активный приём из вашей текущей памяти каждый ход.", r.Drinks))
		}
		line("Присоединиться к боссу? Большинство решает исход; при равенстве учитывается выбор живого главы, иначе первого живого. Молчание к сроку считается отказом.")
		var waiting []string
		count := 0
		for _, a := range r.Players {
			if a.HP > 0 && !a.Absent {
				if _, ok := r.Votes[a.ID]; ok {
					count++
				} else {
					waiting = append(waiting, a.Name)
				}
			}
		}
		line(fmt.Sprintf("\nГолосов принято: %d. Осталось: %d.", count, len(waiting)))
		if v, ok := r.Votes[p.ID]; ok {
			choice := "отказаться"
			if v {
				choice = "присоединиться"
			}
			line("✅ Ваш голос принят: <b>" + choice + "</b>.")
		} else if p.HP > 0 && !p.Absent {
			button("🤝 Присоединиться", "/vote yes")
			button("⚔ Отказаться", "/vote no")
		}
		if len(waiting) > 0 {
			line("⏳ Ждём: " + esc(strings.Join(waiting, ", ")))
		}
	}
	if len(r.Log) > 0 && r.Phase != "lobby" {
		heading("📜 Последние события")
		for _, entry := range r.Log {
			line("• " + esc(entry))
		}
	}
	if r.Phase == "ended" {
		heading("🏁 " + phaseName(r.Outcome))
		if c.Finale != nil {
			ending := map[string]string{"joined": c.Finale.Joined, "slain": c.Finale.Slain, "defeat": c.Finale.Defeat}[r.Outcome]
			line(esc(ending))
			seen := map[string]bool{}
			for _, a := range r.Players {
				bg := c.Background(a.Background)
				if !seen[bg.ID] && slices.Contains(a.Flags, bg.Flag) {
					if text := bg.Endings[r.Outcome]; text != "" {
						line(esc(text))
					}
					seen[bg.ID] = true
				}
			}
		} else {
			flags := map[string]bool{}
			for _, a := range r.Players {
				for _, f := range a.Flags {
					flags[f] = true
				}
			}
			if flags["tunnels"] {
				line("Знание штолен сохранилось в рассказах горняков.")
			}
			if flags["witness"] {
				line("Странник понесёт историю за пределы гор.")
			}
		}
		line("Новый поход: /create пароль или /join КОД пароль")
	}
	if r.Deadline > 0 {
		line("\n⏳ До " + time.Unix(r.Deadline, 0).UTC().Format("15:04:05") + " UTC")
	}
	if buildErr != nil {
		return buildErr
	}
	return enqueueView(tx, p.ID, m)
}

// Every line generated above has balanced HTML. Split long screens without losing actions.
func enqueueView(tx *sql.Tx, user string, m Message) error {
	var chunks []string
	var current string
	for _, line := range strings.Split(m.Text, "\n") {
		if messageUnits(line) > 3800 {
			// Oversized content remains readable and cannot split an HTML entity or tag.
			plain := html.UnescapeString(strings.NewReplacer("<b>", "", "</b>", "").Replace(line))
			runes := []rune(plain)
			for len(runes) > 0 {
				n := min(len(runes), 600)
				if current != "" {
					chunks = append(chunks, current)
					current = ""
				}
				chunks = append(chunks, esc(string(runes[:n])))
				runes = runes[n:]
			}
			continue
		}
		if messageUnits(current)+messageUnits(line)+1 > 3800 {
			chunks = append(chunks, current)
			current = ""
		}
		current += line + "\n"
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	for i, text := range chunks {
		part := m
		part.Text = text
		if len(chunks) > 1 {
			part.Fresh = true
		}
		if i < len(chunks)-1 {
			part.Card = false
			part.Buttons = nil
		}
		if err := enqueue(tx, user, part); err != nil {
			return err
		}
	}
	return nil
}
func backgroundSummary(bg domain.Background) string {
	if bg.Summary != "" {
		return bg.Summary
	}
	return bg.Reply
}
func classIcon(id string) string {
	switch id {
	case "warrior":
		return "🛡"
	case "ranger":
		return "🏹"
	case "wizard":
		return "🔮"
	}
	return "👤"
}
func rarityLabel(s string) string {
	switch s {
	case "common":
		return "⚪ обычная"
	case "rare":
		return "🔵 редкая"
	case "epic":
		return "🟣 эпическая"
	case "legendary":
		return "🟠 легендарная"
	}
	return s
}
func memoryLabel(m domain.Memory) string {
	return strings.Fields(rarityLabel(m.Rarity))[0] + " " + abilityIcon(m) + " " + m.Name
}
func abilityIcon(m domain.Memory) string {
	for _, e := range m.Effects {
		switch e.Kind {
		case "heal":
			return "💚"
		case "poison":
			return "☠️"
		case "temp_hp", "intercept":
			return "🛡"
		case "taunt":
			return "📣"
		case "boost":
			return "✨"
		case "counter":
			return "↩️"
		case "skip":
			return "💫"
		}
	}
	return "⚔"
}
func memoryDescription(m domain.Memory) string {
	kind := "активная"
	if m.Type == "reactive" {
		kind = "реакция"
	} else if m.Type != "active" {
		kind = "пассивная"
	}
	out := "<b>" + esc(memoryLabel(m)) + "</b> · " + esc(rarityLabel(m.Rarity)) + " · " + kind
	var bonuses []string
	if m.Bonus.HP != 0 {
		bonuses = append(bonuses, fmt.Sprintf("макс. HP %+d", m.Bonus.HP))
	}
	if m.Bonus.Attack != 0 {
		bonuses = append(bonuses, fmt.Sprintf("сила %+d", m.Bonus.Attack))
	}
	if m.Bonus.Accuracy != 0 {
		bonuses = append(bonuses, fmt.Sprintf("точность %+d", m.Bonus.Accuracy))
	}
	if len(bonuses) > 0 {
		out += "\n  " + strings.Join(bonuses, ", ")
	}
	if effects := effectText(m.Effects); effects != "" {
		out += "\n  " + esc(effects)
	}
	return out
}
func timedText(es []domain.Timed) string {
	var out []string
	for _, e := range es {
		switch e.Kind {
		case "poison":
			out = append(out, fmt.Sprintf("☠ яд %d · осталось %d", e.Power, e.Left))
		case "skip":
			out = append(out, "💫 пропуск следующего действия")
		case "boost":
			out = append(out, fmt.Sprintf("✨ сила +%d · осталось %d", e.Power, e.Left))
		case "taunt":
			out = append(out, fmt.Sprintf("📣 провокация · осталось %d", e.Left))
		}
	}
	return strings.Join(out, "; ")
}
func phaseName(s string) string {
	names := map[string]string{"lobby": "Лобби", "combat": "Бой", "barrel": "Выбор у бочки", "preparation": "Подготовка у бочки", "rewards": "Награды", "transition": "Перед следующей встречей", "paused": "Пауза", "challenge": "Испытание", "vote": "Голосование", "ended": "Поход завершён", "expired": "Комната истекла", "joined": "Сторона босса принята", "slain": "Босс повержен", "defeat": "Поражение", "crocodile": "Крокодил", "three_p": "Три-П", "anagram": "Анаграмма", "codenames": "Три слова"}
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
		case "damage":
			v = fmt.Sprintf("⚔ урон %d%% силы", e.Power)
		case "counter":
			v = fmt.Sprintf("↩ контратака: %d%% силы", e.Power)
		case "temp_hp":
			v = fmt.Sprintf("🛡 временные HP +%d", e.Power)
		case "poison":
			v = fmt.Sprintf("☠ яд %d, тиков %d, шанс %d%%", e.Power, e.Duration, e.Chance)
		case "skip":
			v = fmt.Sprintf("💫 пропуск действия: шанс %d%%", e.Chance)
		case "heal":
			v = fmt.Sprintf("💚 лечение %d%% максимума, одноразовое", e.Power)
		case "boost":
			v = fmt.Sprintf("✨ сила +%d на %d фазы", e.Power, e.Duration)
		case "taunt":
			v = fmt.Sprintf("📣 провокация на %d фазы", e.Duration)
		case "intercept":
			v = "🛡 перехват атаки союзника"
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, "; ")
}

// Telegram offsets use UTF-16; count astral emoji conservatively as two units.
func messageUnits(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}
