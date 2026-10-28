package source

import "aurumtv/internal/channel"

// Decision of the selection rules.
type Decision struct {
	ActiveID string
	Reason   string
}

// Policy of the selection.
type Policy struct {
	PreferPrimary    bool
	RecoverThreshold int
}

// Choose picks the source a channel should use (pure, testable without a network):
//   - the current source is not down → keep it (one bad probe never switches),
//     unless PreferPrimary and a higher-priority source has recovered RecoverThreshold times in a row;
//   - the current source is down → the first source verified up, by priority;
//     none verified → keep the current one (never switch to an unproven source — no guessing);
//   - no current source (new channel) → the first up source, else the first not down.
func Choose(sources []channel.Source, healthOf func(string) Health, currentID string, p Policy) Decision {
	if len(sources) == 0 {
		return Decision{Reason: "no_sources"}
	}
	var current *channel.Source
	for i := range sources {
		if sources[i].ID == currentID {
			current = &sources[i]
		}
	}
	var firstUp *channel.Source
	for i := range sources {
		if healthOf(sources[i].ID).Status == StatusUp {
			firstUp = &sources[i]
			break
		}
	}

	if current != nil && healthOf(current.ID).Status != StatusDown {
		if p.PreferPrimary && firstUp != nil && firstUp.Priority < current.Priority &&
			healthOf(firstUp.ID).ConsecutiveOks >= p.RecoverThreshold {
			return Decision{ActiveID: firstUp.ID, Reason: "primary_recovered"}
		}
		return Decision{ActiveID: current.ID, Reason: "current_ok"}
	}
	if firstUp != nil {
		if current != nil {
			return Decision{ActiveID: firstUp.ID, Reason: "failover"}
		}
		return Decision{ActiveID: firstUp.ID, Reason: "initial"}
	}
	if current != nil {
		return Decision{ActiveID: current.ID, Reason: "no_healthy_backup"}
	}
	for _, s := range sources {
		if healthOf(s.ID).Status != StatusDown {
			return Decision{ActiveID: s.ID, Reason: "initial"}
		}
	}
	return Decision{ActiveID: sources[0].ID, Reason: "initial"}
}
