package domain

import "slices"

// MemoryReady ignores fallen and absent players, who cannot block preparation.
func (r *Room) MemoryReady() bool {
	for _, p := range r.Players {
		if p.HP > 0 && !p.Absent && !p.Confirmed {
			return false
		}
	}
	return true
}

// FinishMemory keeps committed memory when a deadline expires; drafts are never
// silently committed. Confirmed choices and earned slots are preserved.
func (r *Room) FinishMemory() {
	for i := range r.Players {
		p := &r.Players[i]
		p.Draft = slices.Clone(p.Memory)
		p.Offer = nil
		p.Confirmed = true
	}
}

func (r *Room) Continue(c *Content, rng RNG, now int64) error {
	if !r.MemoryReady() {
		return ErrInvalid
	}
	switch r.Phase {
	case "transition":
		r.Encounter++
		r.Enter(c, now)
	case "preparation":
		if c.Route[r.Encounter].Final {
			r.Votes = map[string]bool{}
			r.note("Босс: мой порядок спасёт горы. Присоединитесь? Молчание — отказ; при равенстве решает живой глава.")
			r.SetPhase("vote", c.Balance.VoteSeconds, now)
		} else {
			r.StartChallenge(c, rng, now)
		}
	default:
		return ErrInvalid
	}
	return nil
}
func (r *Room) StartChallenge(c *Content, rng RNG, now int64) {
	kinds := []string{}
	for _, k := range c.Challenges {
		if !slices.Contains(r.UsedChallenges, k) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		kinds = c.Challenges
	}
	kind := kinds[rng.Intn(len(kinds))]
	r.UsedChallenges = append(r.UsedChallenges, kind)
	live := []string{}
	for _, p := range r.Players {
		if p.HP > 0 && !p.Absent {
			live = append(live, p.ID)
		}
	}
	if len(live) < 2 {
		r.BeginCombat(c, now)
		return
	}
	performer := live[r.PerformerIndex%len(live)]
	r.PerformerIndex++
	pool := []Word{}
	for _, w := range c.Words {
		if slices.Contains(w.Uses, kind) && !slices.Contains(r.UsedWords, w.ID) {
			pool = append(pool, w)
		}
	}
	if len(pool) < 3 {
		pool = nil
		for _, w := range c.Words {
			if slices.Contains(w.Uses, kind) {
				pool = append(pool, w)
			}
		}
	}
	ch := Challenge{Kind: kind, Performer: performer, Captain: performer, Guesser: live[r.PerformerIndex%len(live)], Black: rng.Intn(3)}
	n := 1
	if kind == "codenames" {
		n = 3
	}
	for i := 0; i < n; i++ {
		j := rng.Intn(len(pool))
		w := pool[j]
		pool = append(pool[:j], pool[j+1:]...)
		r.UsedWords = append(r.UsedWords, w.ID)
		ch.Words = append(ch.Words, w.Text)
		if i == 0 {
			ch.Secret = w.ID
			ch.Scramble = w.Text
		}
	}
	if kind == "anagram" {
		rs := []rune(ch.Scramble)
		for i := len(rs) - 1; i > 0; i-- {
			j := rng.Intn(i + 1)
			rs[i], rs[j] = rs[j], rs[i]
		}
		if string(rs) == ch.Scramble && len(rs) > 1 {
			for i := 1; i < len(rs); i++ {
				if rs[i] != rs[0] {
					rs[0], rs[i] = rs[i], rs[0]
					break
				}
			}
		}
		ch.Scramble = string(rs)
	}
	r.Challenge = ch
	r.SetPhase("challenge", c.Balance.ChallengeSeconds, now)
}
func (r *Room) ChallengeInput(c *Content, id, answer string, cell int, now int64) error {
	if r.Phase != "challenge" {
		return ErrInvalid
	}
	ch := &r.Challenge
	win := false
	switch ch.Kind {
	case "crocodile", "three_p":
		if id != ch.Performer || answer != "solved" {
			return ErrInvalid
		}
		win = true
	case "anagram":
		for _, w := range c.Words {
			if w.ID == ch.Secret {
				for _, a := range append([]string{w.Text}, w.Alternatives...) {
					if Normalize(answer) == Normalize(a) {
						win = true
					}
				}
			}
		}
	case "codenames":
		if id != ch.Guesser || cell < 0 || cell >= 3 || slices.Contains(ch.Open, cell) {
			return ErrInvalid
		}
		ch.Open = append(ch.Open, cell)
		if cell == ch.Black {
			r.BeginCombat(c, now)
			return nil
		}
		win = len(ch.Open) == 2
	}
	if win {
		r.Win(c, now)
	}
	return nil
}
func (r *Room) CastVote(c *Content, id string, join bool, now int64) error {
	p := r.Player(id)
	if r.Phase != "vote" || p == nil || p.HP == 0 || p.Absent {
		return ErrInvalid
	}
	if _, voted := r.Votes[id]; voted {
		return ErrInvalid
	}
	if r.Votes == nil {
		r.Votes = map[string]bool{}
	}
	r.Votes[id] = join
	for _, player := range r.Players {
		if player.HP > 0 && !player.Absent {
			if _, voted := r.Votes[player.ID]; !voted {
				return nil
			}
		}
	}
	r.FinishVote(c, now)
	return nil
}

func (r *Room) FinishVote(c *Content, now int64) {
	if r.Phase != "vote" {
		return
	}
	yes := 0
	live := r.livingPlayers()
	for _, i := range live {
		if r.Votes[r.Players[i].ID] {
			yes++
		}
	}
	join := yes*2 > len(live)
	complete := true
	for _, i := range live {
		if _, ok := r.Votes[r.Players[i].ID]; !ok {
			complete = false
		}
	}
	if len(live) > 0 && yes*2 == len(live) && complete {
		tie := r.Player(r.Leader)
		if tie == nil || tie.HP == 0 {
			tie = &r.Players[live[0]]
		}
		join = r.Votes[tie.ID]
	}
	if join {
		r.Outcome = "joined"
		r.SetPhase("ended", 0, now)
		r.note("Вы приняли сторону хозяина тумана.")
	} else {
		r.BeginCombat(c, now)
	}
}
func (r *Room) Timeout(c *Content, rng RNG, now int64) {
	if r.Phase == "ended" || r.Phase == "expired" {
		return
	}
	if now-r.LastAction >= int64(c.Balance.ExpirySeconds) {
		r.SetPhase("expired", 0, now)
		return
	}
	if r.Deadline == 0 || now < r.Deadline {
		return
	}
	switch r.Phase {
	case "combat":
		r.Log = nil
		for _, p := range r.Players {
			if p.HP > 0 && !p.Acted {
				r.note("⏳ " + p.Name + ": время истекло, действие пропущено.")
			}
		}
		r.EndPlayers(c, rng, now)
	case "rewards":
		r.FinishMemory()
		r.SetPhase("transition", c.Balance.PreparationSeconds, now)
	case "preparation", "transition":
		r.FinishMemory()
		_ = r.Continue(c, rng, now)
	case "challenge":
		r.BeginCombat(c, now)
	case "vote":
		r.FinishVote(c, now)
	}
}
