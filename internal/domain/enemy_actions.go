package domain

import (
	"fmt"
	"slices"
)

func enemyAttackPower(c *Content, e *Enemy) int {
	power := c.Enemy(e.Def).Attack
	for _, effect := range e.Effects {
		if effect.Kind == "boost" {
			power += effect.Power
		}
	}
	return power
}

// A normal attack and a copied attack are separate roots. Multi-target hits share
// one root, so interception cannot also trigger a counter on that same attack.
func (r *Room) enemyAttack(c *Content, rng RNG, e *Enemy, m Memory, all bool) {
	live := r.livingPlayers()
	if len(live) == 0 {
		return
	}
	primary := live[rng.Intn(len(live))]
	for _, effect := range e.Effects {
		if effect.Kind == "taunt" {
			for i, p := range r.Players {
				if p.ID == effect.Source && p.HP > 0 {
					primary = i
				}
			}
		}
	}
	targets := []int{primary}
	if all {
		targets = live
	} else if m.Targets > 1 {
		others := slices.DeleteFunc(slices.Clone(live), func(i int) bool { return i == primary })
		if len(others) > 0 {
			targets = append(targets, others[rng.Intn(len(others))])
		}
	}
	rootUsed := map[string]bool{}
	for _, pi := range targets {
		if e.HP == 0 {
			break
		}
		if r.Players[pi].HP == 0 {
			continue
		}
		actual := pi
		for wi := range r.Players {
			w := &r.Players[wi]
			if wi == pi || w.HP == 0 || w.Class != "warrior" || rootUsed[w.ID] {
				continue
			}
			if reaction := firstReaction(c, w, "intercept", "ally"); reaction.ID != "" {
				actual = wi
				w.Used = append(w.Used, reaction.ID)
				rootUsed[w.ID] = true
				r.note("🛡 " + w.Name + " перехватывает атаку на " + r.Players[pi].Name + " (" + reaction.Name + ").")
				break
			}
		}
		p := &r.Players[actual]
		for _, effect := range m.Effects {
			switch effect.Kind {
			case "damage":
				amount := enemyAttackPower(c, e) * effect.Power / 100
				if effect.Power > 0 {
					amount = max(1, amount)
				}
				r.dealDamage(r.enemyName(c, e), p.Name, &p.HP, &p.Temp, amount)
			case "poison", "skip":
				if p.HP > 0 && rng.Intn(100) < effect.Chance {
					apply(&p.Effects, Timed{Kind: effect.Kind, Source: "enemy:" + e.ID, Family: m.Family, Power: effect.Power, Left: effect.Duration, End: "players"})
					if effect.Kind == "poison" {
						r.note(fmt.Sprintf("☠ %s: яд %d на %d фазы.", p.Name, effect.Power, effect.Duration))
					} else {
						r.note("⏭ " + p.Name + ": наложен пропуск следующего действия.")
					}
				}
			}
		}
		if p.HP > 0 && p.Class == "warrior" && !rootUsed[p.ID] {
			if reaction := firstReaction(c, p, "counter", "self"); reaction.ID != "" {
				p.Used = append(p.Used, reaction.ID)
				rootUsed[p.ID] = true
				r.note("↩ " + p.Name + ": контратака (" + reaction.Name + ").")
				r.strike(c, rng, p, reaction, e)
			}
		}
	}
}

func (r *Room) copyMemory(c *Content, rng RNG, e *Enemy) {
	var pool []Memory
	seen := map[string]bool{}
	for _, p := range r.Players {
		for _, id := range p.Memory {
			m := c.Memory(id)
			if m.Type != "active" || seen[id] || (m.Once && slices.Contains(e.CopiedOnce, id)) {
				continue
			}
			seen[id] = true
			pool = append(pool, m)
		}
	}
	if len(pool) == 0 {
		r.note("Алхимик: «Вы даже полезных мыслей мне не оставили!» Заимствовать нечего.")
		return
	}
	m := pool[rng.Intn(len(pool))]
	if !e.CopyAnnounced && c.Finale != nil && c.Finale.CopyLine != "" {
		r.note(c.Finale.CopyLine)
		e.CopyAnnounced = true
	}
	r.note("🧠 Алхимик заимствует «" + m.Name + "» (" + rarityName(m.Rarity) + ").")
	if m.Once {
		e.CopiedOnce = append(e.CopiedOnce, m.ID)
	}
	for _, effect := range m.Effects {
		switch effect.Kind {
		case "temp_hp":
			e.Temp += effect.Power
			r.note(fmt.Sprintf("🛡 Алхимик: +%d временных HP (всего %d).", effect.Power, e.Temp))
		case "heal":
			maxHP := c.Enemy(e.Def).HP * r.PartySize
			before := e.HP
			e.HP = min(maxHP, e.HP+maxHP*effect.Power/100)
			r.note(fmt.Sprintf("💚 Алхимик восстановил %d HP → %d/%d.", e.HP-before, e.HP, maxHP))
		case "boost":
			apply(&e.Effects, Timed{Kind: "boost", Source: "enemy:" + e.ID, Family: m.Family, Power: effect.Power, Left: effect.Duration, End: "enemies"})
			r.note(fmt.Sprintf("💪 Алхимик: сила +%d на %d фазы.", effect.Power, effect.Duration))
		case "taunt":
			// The alchemist is the only remaining enemy: every hostile player
			// ability already targets him. Copying taunt grants no extra damage.
			r.note("📣 Алхимик вызывает всех на себя. Других врагов в этом бою нет.")
		}
	}
	if m.Target == "enemy" {
		r.enemyAttack(c, rng, e, m, false)
	}
}

func rarityName(rarity string) string {
	return map[string]string{"common": "обычная", "rare": "редкая", "epic": "эпическая", "legendary": "легендарная"}[rarity]
}
