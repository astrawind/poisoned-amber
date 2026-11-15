package domain

// BattleEvent is a domain-only extension boundary. Events contain no transport IDs.
// The bounded recent stream can feed summaries or future rendering adapters.
type BattleEvent struct {
	Kind     string
	Actor    string
	Target   string
	Amount   int
	Reaction bool
}

func (r *Room) emit(event BattleEvent) {
	r.Events = append(r.Events, event)
	if len(r.Events) > 64 {
		r.Events = r.Events[len(r.Events)-64:]
	}
}
