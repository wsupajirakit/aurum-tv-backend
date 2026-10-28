// Package clock lets time be injected, so tests can control it.
package clock

import "time"

type Clock interface{ Now() time.Time }

// System is the real clock.
type System struct{}

func (System) Now() time.Time { return time.Now() }

// Unix returns c.Now() in whole seconds — the unit used by tokens and signatures.
func Unix(c Clock) int64 { return c.Now().Unix() }
