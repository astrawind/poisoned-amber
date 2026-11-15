package domain

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

type RNG interface{ Intn(int) int }
type Stats struct{ HP, Attack, Accuracy int }
type Class struct {
	ID, Name, Start string
	Base            Stats
}
type Effect struct {
	Kind                    string
	Power, Duration, Chance int
	End                     string
}
type Memory struct {
	Targets                                                   int
	ID, Family, Name, Rarity, Type, Target, Trigger, MediaKey string
	Classes                                                   []string
	Bonus                                                     Stats
	Effects                                                   []Effect
	Once                                                      bool
}
type Background struct {
	ID, Name, Race, Reply, Flag string
	Tags                        []string
	Summary                     string            `json:",omitempty"`
	Endings                     map[string]string `json:",omitempty"`
}
type Boss struct {
	ID, Name, Enemy, Intro, Challenge, Fight, Victory string
	Replies                                           map[string]string     `json:",omitempty"`
	Dialogues                                         map[string][]Dialogue `json:",omitempty"`
}
type Dialogue struct {
	ID, Line, Response string
}
type Finale struct {
	Minion, Summon, NoMinions, CopyLine, Joined, Slain, Defeat string
	Speeches                                                   []string
}
type EnemyDef struct {
	ID, Name, Strategy, MediaKey string
	HP, Attack                   int
	All                          bool
}
type Encounter struct {
	ID, Name, MediaKey string
	Enemies            []string
	Boss, Final        bool
	Intro              string                `json:",omitempty"`
	BossPool           []string              `json:",omitempty"`
	Dialogues          map[string][]Dialogue `json:",omitempty"`
}
type Word struct {
	ID, Text, Language       string
	Tags, Uses, Alternatives []string
	Difficulty               int
}
type Balance struct {
	CriticalFailure, PhaseSeconds, RewardSeconds, PreparationSeconds, ChallengeSeconds, VoteSeconds, ExpirySeconds int
	RarityWeights                                                                                                  [][]int
}
type Content struct {
	Schema      int
	Version     string
	Classes     []Class
	Memories    []Memory
	Backgrounds []Background
	Enemies     []EnemyDef
	Route       []Encounter
	Words       []Word
	Challenges  []string
	Balance     Balance
	Bosses      []Boss  `json:",omitempty"`
	Finale      *Finale `json:",omitempty"`
	Prologue    string  `json:",omitempty"`
}

var Rarities = []string{"common", "rare", "epic", "legendary"}
var EffectKinds = []string{"damage", "temp_hp", "poison", "skip", "taunt", "boost", "heal", "intercept", "counter"}

func ReadContent(r io.Reader) (*Content, error) {
	var c Content
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if e := d.Decode(&c); e != nil {
		return nil, e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return &c, c.Validate()
}
func (c *Content) Memory(id string) Memory {
	for _, m := range c.Memories {
		if m.ID == id {
			return m
		}
	}
	return Memory{}
}
func (c *Content) Class(id string) Class {
	for _, v := range c.Classes {
		if v.ID == id {
			return v
		}
	}
	return Class{}
}
func (c *Content) Background(id string) Background {
	for _, v := range c.Backgrounds {
		if v.ID == id {
			return v
		}
	}
	return Background{}
}
func (c *Content) Enemy(id string) EnemyDef {
	for _, v := range c.Enemies {
		if v.ID == id {
			return v
		}
	}
	return EnemyDef{}
}
func (c *Content) Boss(id string) Boss {
	for _, b := range c.Bosses {
		if b.ID == id {
			return b
		}
	}
	return Boss{}
}
func (c *Content) Validate() error {
	bad := func(s string) error { return fmt.Errorf("invalid content: %s", s) }
	if (c.Schema != 1 && c.Schema != 2) || c.Version == "" || len(c.Classes) != 3 || len(c.Backgrounds) == 0 || (c.Schema == 1 && len(c.Route) != 9) || (c.Schema == 2 && len(c.Route) != 7) {
		return bad("header/route")
	}
	b := c.Balance
	if b.CriticalFailure < 0 || b.CriticalFailure > 100 || b.PhaseSeconds < 1 || b.RewardSeconds < 1 || b.PreparationSeconds < 1 || b.ChallengeSeconds < 1 || b.VoteSeconds < 1 || b.ExpirySeconds < 1 || len(b.RarityWeights) != 4 {
		return bad("balance")
	}
	for _, row := range b.RarityWeights {
		sum := 0
		if len(row) != 4 {
			return bad("rarity weights")
		}
		for _, n := range row {
			if n < 0 {
				return bad("weight")
			}
			sum += n
		}
		if sum == 0 {
			return bad("empty weights")
		}
	}
	ids := map[string]bool{}
	unique := func(id string) bool {
		if id == "" || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	for _, cl := range c.Classes {
		if !unique(cl.ID) || !slices.Contains([]string{"warrior", "ranger", "wizard"}, cl.ID) || cl.Base.HP < 1 || cl.Base.HP > 100000 || cl.Base.Attack > 100000 || cl.Base.Attack < 1 || cl.Base.Accuracy < 0 || cl.Base.Accuracy+b.CriticalFailure > 100 {
			return bad("class")
		}
	}
	ids = map[string]bool{}
	for _, m := range c.Memories {
		if !unique(m.ID) || m.Family == "" || m.Name == "" || !slices.Contains(Rarities, m.Rarity) || !slices.Contains([]string{"active", "reactive", "passive"}, m.Type) || len(m.Classes) == 0 || m.Bonus.HP < 0 || m.Bonus.HP > 100000 || m.Bonus.Attack > 100000 || m.Bonus.Accuracy > 100 || m.Bonus.Attack < 0 || m.Bonus.Accuracy < 0 {
			return bad("memory " + m.ID)
		}
		if !slices.Contains([]string{"self", "enemy", "ally", "team"}, m.Target) {
			return bad("target")
		}
		for _, cl := range m.Classes {
			if c.Class(cl).ID == "" {
				return bad("memory class")
			}
			if m.Type == "reactive" && cl != "warrior" {
				return bad("reaction class")
			}
		}
		if m.Targets < 0 || m.Targets > 2 {
			return bad("target count")
		}
		if m.Targets > 1 {
			for _, cl := range m.Classes {
				if cl != "wizard" {
					return bad("only wizard may attack multiple targets")
				}
			}
		}
		for _, e := range m.Effects {
			if e.Power > 100000 || e.Duration > 1000 {
				return bad("effect limits")
			}
			if slices.Contains([]string{"damage", "poison", "skip"}, e.Kind) && m.Target != "enemy" {
				return bad("hostile target")
			}
			if e.Kind == "boost" && !slices.Contains([]string{"self", "ally", "team"}, m.Target) {
				return bad("boost target")
			}
			if e.Kind == "heal" && (!m.Once || m.Target != "self" || e.Power > 100) {
				return bad("healing")
			}
			if e.Kind == "intercept" && m.Trigger != "ally" || e.Kind == "counter" && m.Trigger != "self" {
				return bad("reaction trigger")
			}

			if !slices.Contains(EffectKinds, e.Kind) || e.Power < 0 || e.Duration < 0 || e.Chance < 0 || e.Chance > 100 {
				return bad("effect")
			}
			if slices.Contains([]string{"poison", "boost", "taunt"}, e.Kind) && (e.Duration < 1 || !slices.Contains([]string{"players", "enemies"}, e.End)) {
				return bad("effect duration")
			}
			if slices.Contains([]string{"counter", "intercept"}, e.Kind) && m.Type != "reactive" {
				return bad("trigger")
			}
		}
		if m.Type == "reactive" && !slices.Contains([]string{"self", "ally"}, m.Trigger) {
			return bad("reaction trigger")
		}
	}
	for _, cl := range c.Classes {
		m := c.Memory(cl.Start)
		if m.ID == "" || m.Type != "active" || m.Rarity != "common" || !slices.Contains(m.Classes, cl.ID) {
			return bad("start")
		}
	}
	ids = map[string]bool{}
	for _, bg := range c.Backgrounds {
		if !unique(bg.ID) || bg.Race == "" {
			return bad("background")
		}
		for outcome := range bg.Endings {
			if !slices.Contains([]string{"joined", "slain", "defeat"}, outcome) {
				return bad("background ending")
			}
		}
	}
	ids = map[string]bool{}
	for _, e := range c.Enemies {
		if !unique(e.ID) || e.HP < 1 || e.HP > 100000 || e.Attack < 1 || e.Attack > 100000 || (e.Strategy != "random" && (c.Schema != 2 || e.Strategy != "alchemist")) {
			return bad("enemy")
		}
	}
	ids = map[string]bool{}
	dialogueIDs := map[string]bool{}
	validateDialogues := func(dialogues map[string][]Dialogue, responseRequired bool) error {
		if len(dialogues) > 0 && c.Schema != 2 {
			return bad("dialogue schema")
		}
		for background, pool := range dialogues {
			if c.Background(background).ID == "" || len(pool) == 0 {
				return bad("dialogue background/pool")
			}
			lines := map[string]bool{}
			for _, d := range pool {
				line := Normalize(d.Line)
				if Normalize(d.ID) == "" || dialogueIDs[d.ID] || line == "" || lines[line] || (responseRequired && Normalize(d.Response) == "") {
					return bad("dialogue")
				}
				dialogueIDs[d.ID], lines[line] = true, true
			}
		}
		return nil
	}
	for _, b := range c.Bosses {
		if !unique(b.ID) || b.Name == "" || c.Enemy(b.Enemy).ID == "" || c.Enemy(b.Enemy).Strategy != "random" || b.Intro == "" || b.Challenge == "" || b.Fight == "" || b.Victory == "" {
			return bad("boss")
		}
		for id := range b.Replies {
			if c.Background(id).ID == "" {
				return bad("boss background")
			}
		}
		if err := validateDialogues(b.Dialogues, true); err != nil {
			return err
		}
	}
	if c.Schema == 1 && (len(c.Bosses) != 0 || c.Finale != nil) {
		return bad("legacy extensions")
	}
	if c.Schema == 2 {
		if c.Finale == nil || c.Enemy(c.Finale.Minion).ID == "" || c.Enemy(c.Finale.Minion).Strategy != "random" || len(c.Finale.Speeches) == 0 || c.Finale.Joined == "" || c.Finale.Slain == "" || c.Finale.Defeat == "" || c.Finale.Summon == "" || c.Finale.NoMinions == "" || c.Finale.CopyLine == "" {
			return bad("finale")
		}
		for _, speech := range c.Finale.Speeches {
			if speech == "" {
				return bad("final speech")
			}
		}
	}
	ids = map[string]bool{}
	for i, e := range c.Route {
		final := i == len(c.Route)-1
		if !unique(e.ID) || e.Boss != (i%3 == 2 || final) || e.Final != final || (len(e.Enemies) == 0 && len(e.BossPool) == 0) {
			return bad("encounter")
		}
		if len(e.BossPool) > 0 && len(e.Dialogues) > 0 {
			return bad("dialogue on pooled encounter")
		}
		if err := validateDialogues(e.Dialogues, e.Boss); err != nil {
			return err
		}
		if len(e.BossPool) > 0 {
			if c.Schema != 2 || !e.Boss || e.Final || len(e.Enemies) != 0 {
				return bad("boss pool location")
			}
			seen := map[string]bool{}
			for _, id := range e.BossPool {
				if c.Boss(id).ID == "" || seen[id] {
					return bad("boss pool")
				}
				seen[id] = true
			}
		}
		if c.Schema == 2 && e.Final && (len(e.Enemies) != 1 || c.Enemy(e.Enemies[0]).Strategy != "alchemist") {
			return bad("final alchemist")
		}
		for _, id := range e.Enemies {
			if c.Enemy(id).ID == "" {
				return bad("enemy reference")
			}
			if !e.Final && c.Enemy(id).Strategy == "alchemist" {
				return bad("alchemist outside finale")
			}
		}
	}
	ids = map[string]bool{}
	texts := map[string]bool{}
	for _, w := range c.Words {
		if !unique(w.ID) || w.Text == "" || texts[Normalize(w.Text)] || w.Language != "ru" || w.Difficulty < 1 || len(w.Uses) == 0 {
			return bad("word")
		}
		texts[Normalize(w.Text)] = true
		if slices.Contains(w.Uses, "anagram") {
			distinct := map[rune]bool{}
			for _, r := range Normalize(w.Text) {
				distinct[r] = true
			}
			if len(distinct) < 2 {
				return bad("anagram cannot be shuffled")
			}
		}
		for _, use := range w.Uses {
			if !slices.Contains([]string{"crocodile", "three_p", "anagram", "codenames"}, use) {
				return bad("word use")
			}
		}
	}
	if len(c.Challenges) != 4 {
		return bad("challenges")
	}
	for _, kind := range []string{"crocodile", "three_p", "anagram", "codenames"} {
		if !slices.Contains(c.Challenges, kind) {
			return bad("challenge handler")
		}
		n := 0
		for _, w := range c.Words {
			if slices.Contains(w.Uses, kind) {
				n++
			}
		}
		if n < 3 {
			return bad("word bank")
		}
	}
	return nil
}
