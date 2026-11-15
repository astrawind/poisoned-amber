package domain

import (
	"fmt"
	"slices"
)

// Encounter choices are drawn once at start and saved with the room, never in rendering.
func (r *Room) CurrentBoss(c *Content) Boss {
	return c.Boss(r.BossChoices[c.Route[r.Encounter].ID])
}

func (r *Room) CurrentEncounter(c *Content) Encounter {
	e := c.Route[r.Encounter]
	if b := r.CurrentBoss(c); b.ID != "" {
		e.Name, e.Intro, e.Enemies = b.Name, b.Intro, []string{b.Enemy}
		e.Dialogues = b.Dialogues
	}
	return e
}

func (r *Room) BarrelReady() bool {
	for _, p := range r.Players {
		if _, done := r.BarrelChoices[p.ID]; !p.Absent && !done {
			return false
		}
	}
	return true
}

func (r *Room) ChooseBarrel(c *Content, id string, drink bool, now int64) error {
	p := r.Player(id)
	if c.Schema != 2 || r.Phase != "barrel" || p == nil || p.Absent {
		return ErrInvalid
	}
	if _, done := r.BarrelChoices[id]; done {
		return ErrInvalid
	}
	if r.BarrelChoices == nil {
		r.BarrelChoices = map[string]bool{}
	}
	r.BarrelChoices[id] = drink
	if drink {
		p.HP = p.Stats(c).HP
		r.Drinks++
		r.note(fmt.Sprintf("🍺 %s восстановил HP. Пивных миньонов в финале: %d.", p.Name, r.Drinks))
	} else {
		r.note(p.Name + " отказался от бочки.")
	}
	if r.BarrelReady() {
		r.FinishBarrel(c, now)
	}
	return nil
}

// Silence and absence never consent to drinking. Fallen players may explicitly drink.
func (r *Room) FinishBarrel(c *Content, now int64) {
	if r.Phase != "barrel" {
		return
	}
	if r.BarrelChoices == nil {
		r.BarrelChoices = map[string]bool{}
	}
	for i := range r.Players {
		p := &r.Players[i]
		if _, done := r.BarrelChoices[p.ID]; !done {
			r.BarrelChoices[p.ID] = false
		}
		p.Effects, p.Temp = nil, 0
		p.Draft, p.Offer = slices.Clone(p.Memory), nil
		p.Confirmed = p.Absent || p.HP == 0
	}
	r.note("Выбор у бочки завершён. Подготовьте память; неответившие сохранили прежнее здоровье.")
	r.SetPhase("preparation", c.Balance.PreparationSeconds, now)
}

func (r *Room) BeginFinal(c *Content, now int64) {
	if c.Finale == nil {
		r.BeginCombat(c, now)
		return
	}
	r.FinalStage = "alchemist"
	if r.Drinks == 0 {
		r.note(c.Finale.NoMinions)
		r.BeginCombat(c, now)
		return
	}
	r.FinalStage = "minions"
	r.note(c.Finale.Summon)
	r.Enemies = nil
	d := c.Enemy(c.Finale.Minion)
	for i := 0; i < r.Drinks; i++ {
		// Their number already scales with party size; do not multiply each one's HP again.
		r.Enemies = append(r.Enemies, Enemy{ID: fmt.Sprintf("minion-%d", i), Def: d.ID, HP: d.HP})
	}
	r.Round = 0
	r.NewRound(c, now)
}

func (r *Room) TellStory(c *Content, rng RNG, id string) error {
	p := r.Player(id)
	if p == nil || p.Absent || (r.Phase != "preparation" && r.Phase != "transition") {
		return ErrInvalid
	}
	key := fmt.Sprintf("%d:%s", r.Encounter, id)
	if r.StoryUsed[key] || r.StoryChoices[key] != "" {
		return ErrInvalid
	}
	if r.StoryUsed == nil {
		r.StoryUsed = map[string]bool{}
	}
	r.StoryUsed[key] = true
	bg := c.Background(p.Background)
	if bg.Flag != "" && !slices.Contains(p.Flags, bg.Flag) {
		p.Flags = append(p.Flags, bg.Flag)
	}
	line, response := bg.Reply, r.CurrentBoss(c).Replies[bg.ID]
	if pool := r.CurrentEncounter(c).Dialogues[bg.ID]; len(pool) > 0 {
		// Equal backgrounds share a pool: exhaust its variants before repeating.
		used := map[string]bool{}
		for _, choice := range r.StoryChoices {
			used[choice] = true
		}
		var available []Dialogue
		for _, d := range pool {
			if !used[d.ID] {
				available = append(available, d)
			}
		}
		if len(available) == 0 {
			available = pool
		}
		d := available[rng.Intn(len(available))]
		if r.StoryChoices == nil {
			r.StoryChoices = map[string]string{}
		}
		r.StoryChoices[key] = d.ID
		line, response = d.Line, d.Response
	}
	r.note(p.Name + ": " + line)
	if response != "" {
		r.note(response)
	}
	return nil
}
