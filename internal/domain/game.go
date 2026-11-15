package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrInvalid = errors.New("действие недоступно или устарело")

type Timed struct {
	Kind, Source, Family, End string
	Power, Left               int
}
type Fighter struct {
	ID, Name, Class, Background string
	HP, Temp, Slots             int
	Memory                      []string
	Effects                     []Timed
	Used                        []string
	Acted, Ready, Absent        bool
	Offer, Draft                []string
	Confirmed                   bool
	Flags                       []string
}
type Enemy struct {
	ID, Def  string
	HP, Temp int
	Effects  []Timed
}
type Challenge struct {
	Kind, Secret, Scramble, Performer, Captain, Guesser string
	Words                                               []string
	Black                                               int
	Open                                                []int
}
type Room struct {
	Events                                                                  []BattleEvent
	ID, Code, Password, Leader, ContentVersion, Phase, PausedPhase, Outcome string
	Players                                                                 []Fighter
	Enemies                                                                 []Enemy
	PhaseID, Round, Encounter, Bosses, PartySize                            int
	Deadline, LastAction, Remaining                                         int64
	Challenge                                                               Challenge
	UsedWords, UsedChallenges                                               []string
	PerformerIndex                                                          int
	Votes                                                                   map[string]bool
	Log                                                                     []string
}

func Normalize(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "ё", "е")
}
func (r *Room) Player(id string) *Fighter {
	for i := range r.Players {
		if r.Players[i].ID == id {
			return &r.Players[i]
		}
	}
	return nil
}
func (p *Fighter) Stats(c *Content) Stats {
	s := c.Class(p.Class).Base
	for _, id := range p.Memory {
		b := c.Memory(id).Bonus
		s.HP += b.HP
		s.Attack += b.Attack
		s.Accuracy += b.Accuracy
	}
	for _, e := range p.Effects {
		if e.Kind == "boost" {
			s.Attack += e.Power
		}
	}
	s.HP = max(1, s.HP)
	s.Accuracy = max(0, min(100-c.Balance.CriticalFailure, s.Accuracy))
	return s
}
func (p *Fighter) Clamp(c *Content) { p.HP = min(p.HP, p.Stats(c).HP) }
func (r *Room) note(s string) {
	r.Log = append(r.Log, s)
	if len(r.Log) > 40 {
		r.Log = r.Log[len(r.Log)-40:]
	}
}
func (r *Room) SetPhase(s string, seconds int, now int64) {
	r.Phase = s
	r.PhaseID++
	r.Deadline = now + int64(seconds)
	if seconds == 0 {
		r.Deadline = 0
	}
}
func (r *Room) Start(c *Content, now int64) error {
	if r.Phase != "lobby" || len(r.Players) < 2 || len(r.Players) > 7 {
		return ErrInvalid
	}
	for _, p := range r.Players {
		if !p.Ready || p.Absent || c.Class(p.Class).ID == "" || c.Background(p.Background).ID == "" {
			return ErrInvalid
		}
	}
	r.PartySize = len(r.Players)
	r.Encounter = 0
	for i := range r.Players {
		p := &r.Players[i]
		p.Memory = []string{c.Class(p.Class).Start}
		p.Slots = 2
		p.HP = p.Stats(c).HP
		p.Draft = slices.Clone(p.Memory)
	}
	r.Enter(c, now)
	return nil
}
func (r *Room) Enter(c *Content, now int64) {
	e := c.Route[r.Encounter]
	r.note(e.Name)
	if e.Boss {
		for i := range r.Players {
			p := &r.Players[i]
			p.Effects = nil
			p.Temp = 0
			p.HP = p.Stats(c).HP
			p.Draft = slices.Clone(p.Memory)
			p.Offer = nil
			p.Confirmed = p.Absent
		}
		r.note("Бочка восстановила всех. Подготовьте память.")
		r.SetPhase("preparation", c.Balance.PreparationSeconds, now)
	} else {
		r.BeginCombat(c, now)
	}
}
func (r *Room) BeginCombat(c *Content, now int64) {
	r.Enemies = nil
	for i, id := range c.Route[r.Encounter].Enemies {
		d := c.Enemy(id)
		r.Enemies = append(r.Enemies, Enemy{ID: fmt.Sprint(i), Def: id, HP: d.HP * r.PartySize})
	}
	r.Round = 0
	r.NewRound(c, now)
}
func (r *Room) NewRound(c *Content, now int64) {
	r.Round++
	r.emit(BattleEvent{Kind: "phase_start"})
	for i := range r.Players {
		p := &r.Players[i]
		p.Used = nil
		p.Acted = p.HP == 0 || p.Absent
		if p.HP > 0 && consume(&p.Effects, "skip") {
			p.Acted = true
			r.note(p.Name + " пропускает действие")
		}
	}
	r.SetPhase("combat", c.Balance.PhaseSeconds, now)
}
func consume(es *[]Timed, kind string) bool {
	for i, e := range *es {
		if e.Kind == kind {
			*es = append((*es)[:i], (*es)[i+1:]...)
			return true
		}
	}
	return false
}
func damage(hp, temp *int, n int) {
	n = max(0, n)
	a := min(*temp, n)
	*temp -= a
	*hp = max(0, *hp-(n-a))
}
func apply(es *[]Timed, e Timed) {
	for i, old := range *es {
		if old.Kind == e.Kind && old.Source == e.Source && old.Family == e.Family {
			e.Power = max(e.Power, old.Power)
			if e.Kind == "taunt" {
				*es = append(append((*es)[:i], (*es)[i+1:]...), e)
			} else {
				(*es)[i] = e
			}
			return
		}
	}
	*es = append(*es, e)
}
func (r *Room) livingPlayers() []int {
	var v []int
	for i, p := range r.Players {
		if p.HP > 0 {
			v = append(v, i)
		}
	}
	return v
}
func (r *Room) livingEnemies() []int {
	var v []int
	for i, e := range r.Enemies {
		if e.HP > 0 {
			v = append(v, i)
		}
	}
	return v
}
func (r *Room) Check(c *Content, now int64) bool {
	if len(r.livingPlayers()) == 0 {
		r.Outcome = "defeat"
		r.SetPhase("ended", 0, now)
		r.note("Команда пала.")
		return true
	}
	if len(r.livingEnemies()) == 0 {
		r.Win(c, now)
		return true
	}
	return false
}
func (r *Room) Win(c *Content, now int64) {
	for i := range r.Players {
		r.Players[i].Effects = nil
		r.Players[i].Temp = 0
		r.Players[i].Clamp(c)
	}
	e := c.Route[r.Encounter]
	if e.Final {
		r.Outcome = "slain"
		r.SetPhase("ended", 0, now)
		r.note("Источник отравления повержен.")
		return
	}
	if e.Boss {
		r.Bosses++
		for i := range r.Players {
			r.Players[i].Slots++
		}
	}
	r.SetPhase("rewards", c.Balance.RewardSeconds, now)
}
func (r *Room) Offers(c *Content, rng RNG) {
	for i := range r.Players {
		p := &r.Players[i]
		p.Offer = nil
		p.Draft = slices.Clone(p.Memory)
		p.Confirmed = p.HP == 0 || p.Absent
		if p.HP == 0 {
			continue
		}
		n := 2
		if c.Route[r.Encounter].Boss {
			n = 3
		}
		pool := []Memory{}
		for _, m := range c.Memories {
			if slices.Contains(m.Classes, p.Class) && !slices.Contains(p.Memory, m.ID) {
				pool = append(pool, m)
			}
		}
		for len(pool) > 0 && len(p.Offer) < n {
			weights := c.Balance.RarityWeights[min(r.Bosses, 3)]
			sum := 0
			for _, m := range pool {
				sum += weights[slices.Index(Rarities, m.Rarity)]
			}
			if sum == 0 {
				break
			}
			v := rng.Intn(sum)
			idx := 0
			for j, m := range pool {
				v -= weights[slices.Index(Rarities, m.Rarity)]
				if v < 0 {
					idx = j
					break
				}
			}
			p.Offer = append(p.Offer, pool[idx].ID)
			pool = append(pool[:idx], pool[idx+1:]...)
		}
	}
}
func (r *Room) SetMemory(c *Content, p *Fighter, ids []string) error {
	if !slices.Contains([]string{"rewards", "preparation", "transition"}, r.Phase) || len(ids) > p.Slots || p.Confirmed && r.Phase == "rewards" {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || (!slices.Contains(p.Memory, id) && !(r.Phase == "rewards" && slices.Contains(p.Offer, id))) {
			return ErrInvalid
		}
		seen[id] = true
	}
	p.Memory = slices.Clone(ids)
	p.Clamp(c)
	return nil
}
func (r *Room) Act(c *Content, rng RNG, id, mem, target string, now int64) error {
	p := r.Player(id)
	if r.Phase != "combat" || p == nil || p.HP == 0 || p.Acted || p.Absent {
		return ErrInvalid
	}
	if mem == "skip" {
		r.Log = nil
		p.Acted = true
		r.note(p.Name + " пропускает")
		r.AfterAction(c, rng, now)
		return nil
	}
	m := c.Memory(mem)
	if !slices.Contains(p.Memory, mem) || m.Type != "active" {
		return ErrInvalid
	}
	var enemy *Enemy
	var ally *Fighter
	switch m.Target {
	case "enemy":
		for i := range r.Enemies {
			if r.Enemies[i].ID == target && r.Enemies[i].HP > 0 {
				enemy = &r.Enemies[i]
			}
		}
		if enemy == nil {
			return ErrInvalid
		}
	case "self":
		ally = p
	case "ally":
		ally = r.Player(target)
		if ally == nil || ally.HP == 0 {
			return ErrInvalid
		}
	case "team":
	default:
		return ErrInvalid
	}
	r.Log = nil
	p.Acted = true
	r.emit(BattleEvent{Kind: "action", Actor: p.ID})
	r.note(p.Name + ": " + m.Name)
	for _, e := range m.Effects {
		switch e.Kind {
		case "temp_hp":
			p.Temp += e.Power
			r.note(fmt.Sprintf("🛡 %s: +%d временных HP (всего %d).", p.Name, e.Power, p.Temp))
		case "heal":
			before := p.HP
			p.HP = min(p.Stats(c).HP, p.HP+p.Stats(c).HP*e.Power/100)
			r.note(fmt.Sprintf("💚 %s: восстановлено %d HP → %d/%d.", p.Name, p.HP-before, p.HP, p.Stats(c).HP))
		case "boost":
			targets := []*Fighter{ally}
			if m.Target == "team" {
				targets = nil
				for i := range r.Players {
					if r.Players[i].HP > 0 {
						targets = append(targets, &r.Players[i])
					}
				}
			}
			for _, a := range targets {
				apply(&a.Effects, Timed{Kind: e.Kind, Source: p.ID, Family: m.Family, Power: e.Power, Left: e.Duration, End: e.End})
				r.note(fmt.Sprintf("💪 %s: усиление силы +%d на %d фазы.", a.Name, e.Power, e.Duration))
			}
		case "taunt":
			for i := range r.Enemies {
				apply(&r.Enemies[i].Effects, Timed{Kind: "taunt", Source: p.ID, Family: m.Family, Left: e.Duration, End: e.End})
			}
			r.note(fmt.Sprintf("📣 %s провоцирует врагов на %d фазы.", p.Name, e.Duration))
		}
	}
	if enemy != nil {
		targets := []*Enemy{enemy}
		if m.Targets > 1 {
			others := []int{}
			for i, e := range r.Enemies {
				if e.HP > 0 && e.ID != enemy.ID {
					others = append(others, i)
				}
			}
			if len(others) > 0 {
				targets = append(targets, &r.Enemies[others[rng.Intn(len(others))]])
			}
		}
		for _, t := range targets {
			r.strike(c, rng, p, m, t)
		}
	}
	if m.Once {
		p.Memory = slices.Delete(p.Memory, slices.Index(p.Memory, m.ID), slices.Index(p.Memory, m.ID)+1)
		p.Clamp(c)
		r.note(fmt.Sprintf("🧪 %s: воспоминание израсходовано; после снятия его бонусов у %s HP %d/%d.", m.Name, p.Name, p.HP, p.Stats(c).HP))
	}
	r.AfterAction(c, rng, now)
	return nil
}
func (r *Room) strike(c *Content, rng RNG, p *Fighter, m Memory, t *Enemy) {
	roll := rng.Intn(100)
	var friend *Fighter
	if roll >= p.Stats(c).Accuracy {
		if roll >= p.Stats(c).Accuracy+c.Balance.CriticalFailure {
			r.note("💨 " + p.Name + " → " + r.enemyName(c, t) + ": промах.")
			return
		}
		choices := []string{"void"}
		var others []*Enemy
		for i := range r.Enemies {
			e := &r.Enemies[i]
			if e.HP > 0 && e.ID != t.ID {
				others = append(others, e)
			}
		}
		var friends []*Fighter
		for i := range r.Players {
			a := &r.Players[i]
			if a.HP > 0 && a.ID != p.ID {
				friends = append(friends, a)
			}
		}
		if len(others) > 0 {
			choices = append(choices, "enemy")
		}
		if len(friends) > 0 {
			choices = append(choices, "ally")
		}
		switch choices[rng.Intn(len(choices))] {
		case "void":
			r.note("💥 " + p.Name + ": критическая неудача — удар в пустоту.")
			return
		case "enemy":
			t = others[rng.Intn(len(others))]
			r.note("💥 " + p.Name + ": критическая неудача — другая цель: " + r.enemyName(c, t) + ".")
		case "ally":
			friend = friends[rng.Intn(len(friends))]
			r.note("💥 " + p.Name + ": критическая неудача — попадание в союзника " + friend.Name + ".")
		}
	}
	for _, e := range m.Effects {
		var hp, temp *int
		var effects *[]Timed
		targetName := r.enemyName(c, t)
		if friend != nil {
			targetName = friend.Name
			hp = &friend.HP
			temp = &friend.Temp
			effects = &friend.Effects
		} else {
			hp = &t.HP
			temp = &t.Temp
			effects = &t.Effects
		}
		switch e.Kind {
		case "damage", "counter":
			n := p.Stats(c).Attack * e.Power / 100
			if e.Power > 0 {
				n = max(1, n)
			}
			r.emit(BattleEvent{Kind: "before_damage", Actor: p.ID, Amount: n, Reaction: m.Type == "reactive"})
			r.dealDamage(p.Name, targetName, hp, temp, n)
			r.emit(BattleEvent{Kind: "after_damage", Actor: p.ID, Amount: n, Reaction: m.Type == "reactive"})
		case "poison", "skip":
			if rng.Intn(100) < e.Chance {
				end := e.End
				if friend != nil && e.Kind == "poison" {
					end = "players"
				}
				apply(effects, Timed{Kind: e.Kind, Source: p.ID, Family: m.Family, Power: e.Power, Left: e.Duration, End: end})
				if e.Kind == "poison" {
					power := e.Power
					for _, active := range *effects {
						if active.Kind == e.Kind && active.Source == p.ID && active.Family == m.Family {
							power = active.Power
						}
					}
					r.note(fmt.Sprintf("☠ %s: яд %d, осталось тиков %d.", targetName, power, e.Duration))
				} else {
					r.note("⏭ " + targetName + ": наложен пропуск следующего действия.")
				}
			} else {
				label := "яд"
				if e.Kind == "skip" {
					label = "пропуск действия"
				}
				r.note("▫ " + targetName + ": " + label + " не сработал.")
			}
		}
	}
}

func (r *Room) enemyName(c *Content, e *Enemy) string {
	for i := range r.Enemies {
		if r.Enemies[i].ID == e.ID {
			return fmt.Sprintf("%s #%d", c.Enemy(e.Def).Name, i+1)
		}
	}
	return c.Enemy(e.Def).Name
}

func (r *Room) dealDamage(actor, target string, hp, temp *int, amount int) {
	beforeHP, beforeTemp := *hp, *temp
	damage(hp, temp, amount)
	r.note(fmt.Sprintf("⚔ %s → %s: урон %d · потеря HP %d · поглощено %d · осталось HP %d.", actor, target, amount, beforeHP-*hp, beforeTemp-*temp, *hp))
	if beforeHP > 0 && *hp == 0 {
		r.note("💀 " + target + " выбывает.")
	}
}

// Keep the effect mechanics in tick; report its actual outcome, including shield absorption.
func (r *Room) tickEffects(name string, hp, temp *int, es *[]Timed, side string) {
	beforeHP, beforeTemp := *hp, *temp
	poison := false
	for _, e := range *es {
		if e.End != side {
			continue
		}
		poison = poison || e.Kind == "poison"
		if e.Left == 1 && e.Kind != "skip" {
			label := map[string]string{"poison": "яд", "boost": "усиление", "taunt": "провокация"}[e.Kind]
			if label != "" {
				r.note("⌛ " + name + ": " + label + " завершается после этой фазы.")
			}
		}
	}
	tick(hp, temp, es, side)
	if poison {
		r.note(fmt.Sprintf("☠ %s: тик яда · потеря HP %d · поглощено %d · осталось HP %d.", name, beforeHP-*hp, beforeTemp-*temp, *hp))
		if beforeHP > 0 && *hp == 0 {
			r.note("💀 " + name + " выбывает от яда.")
		}
	}
}

func tick(hp, temp *int, es *[]Timed, side string) {
	for _, e := range *es {
		if e.Kind == "poison" && e.End == side {
			damage(hp, temp, e.Power)
		}
	}
	out := (*es)[:0]
	for _, e := range *es {
		if e.End == side {
			e.Left--
		}
		if e.Left > 0 || e.Kind == "skip" {
			out = append(out, e)
		}
	}
	*es = out
}
func (r *Room) AfterAction(c *Content, rng RNG, now int64) {
	if r.Check(c, now) {
		return
	}
	for _, p := range r.Players {
		if p.HP > 0 && !p.Acted {
			return
		}
	}
	r.EndPlayers(c, rng, now)
}
func (r *Room) EndPlayers(c *Content, rng RNG, now int64) {
	r.emit(BattleEvent{Kind: "phase_end"})
	for i := range r.Players {
		p := &r.Players[i]
		if p.HP > 0 {
			r.tickEffects(p.Name, &p.HP, &p.Temp, &p.Effects, "players")
		}
	}
	if r.Check(c, now) {
		return
	}
	for ei := range r.Enemies {
		e := &r.Enemies[ei]
		if e.HP == 0 {
			continue
		}
		if consume(&e.Effects, "skip") {
			r.note("⏭ " + r.enemyName(c, e) + ": пропускает действие из-за эффекта.")
			continue
		}
		d := c.Enemy(e.Def)
		live := r.livingPlayers()
		if len(live) == 0 {
			break
		}
		targets := []int{live[rng.Intn(len(live))]}
		if d.All {
			targets = live
		} else {
			for _, eff := range e.Effects {
				if eff.Kind == "taunt" {
					for pi, p := range r.Players {
						if p.ID == eff.Source && p.HP > 0 {
							targets = []int{pi}
						}
					}
				}
			}
		}
		rootUsed := map[string]bool{}
		for _, pi := range targets {
			if r.Players[pi].HP == 0 {
				continue
			}
			actual := pi
			// Before damage only interception is eligible. Join order resolves competing protectors.
			for wi := range r.Players {
				w := &r.Players[wi]
				if wi == pi || w.HP == 0 || w.Class != "warrior" || rootUsed[w.ID] {
					continue
				}
				m := firstReaction(c, w, "intercept", "ally")
				if m.ID != "" {
					actual = wi
					w.Used = append(w.Used, m.ID)
					rootUsed[w.ID] = true
					r.note("🛡 " + w.Name + " перехватывает атаку на " + r.Players[pi].Name + " (" + m.Name + ").")
					break
				}
			}
			p := &r.Players[actual]
			r.dealDamage(r.enemyName(c, e), p.Name, &p.HP, &p.Temp, d.Attack)
			if p.HP > 0 && p.Class == "warrior" && !rootUsed[p.ID] {
				m := firstReaction(c, p, "counter", "self")
				if m.ID != "" {
					p.Used = append(p.Used, m.ID)
					rootUsed[p.ID] = true
					r.note("↩ " + p.Name + ": контратака (" + m.Name + ").")
					r.strike(c, rng, p, m, e)
				}
			}
			if len(r.livingPlayers()) == 0 {
				break
			}
		}
		if r.Check(c, now) {
			return
		}
	}
	for i := range r.Enemies {
		e := &r.Enemies[i]
		if e.HP > 0 {
			r.tickEffects(r.enemyName(c, e), &e.HP, &e.Temp, &e.Effects, "enemies")
		}
	}
	for i := range r.Players {
		p := &r.Players[i]
		if p.HP > 0 {
			r.tickEffects(p.Name, &p.HP, &p.Temp, &p.Effects, "enemies")
		}
		if p.Temp > 0 {
			r.note(fmt.Sprintf("⌛ %s: истекли временные HP (%d).", p.Name, p.Temp))
		}
		p.Temp = 0
	}
	if r.Check(c, now) {
		return
	}
	r.NewRound(c, now)
}

func firstReaction(c *Content, p *Fighter, kind, trigger string) Memory {
	for _, id := range p.Memory {
		m := c.Memory(id)
		if m.Type != "reactive" || m.Trigger != trigger || slices.Contains(p.Used, id) {
			continue
		}
		for _, e := range m.Effects {
			if e.Kind == kind {
				return m
			}
		}
	}
	return Memory{}
}
