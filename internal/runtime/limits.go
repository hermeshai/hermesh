package runtime

import "time"

// Limits is what an agent says of its account's rate limits
// (docs/overview-usage.md §7), told to StartSpec.Limits: a callback, not an
// Event, as they are the account's, not the session's, and stay out of its
// event log.
type Limits struct {
	Plan    string
	Windows []LimitWindow // by group as the agent gives them, the shortest first in each
}

// LimitWindow is one of them: how long it is, how much of it is used, and
// when it starts again.
type LimitWindow struct {
	Minutes     uint32
	UsedPercent uint32 // 0 to 100
	ResetsAt    time.Time
	// Group is the models it counts, when the agent counts groups of models
	// apart (Antigravity); "" for the account's.
	Group string
}
