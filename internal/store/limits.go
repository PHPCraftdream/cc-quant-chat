package store

// Limits per source and quarantine (docs/design-trust.md sections 4 and 5).

// QuarantineInfo: a source whose new events are not taken.
type QuarantineInfo struct {
	Origin string
	Reason string
	Since  string // RFC 3339
	By     string // "owner" or "auto"
	Via    string // the neighbor that brought it, when it is not a neighbor itself
}

// DayStats is what was taken from a source in a day, and how many times a limit was breached.
type DayStats struct {
	Events     int64
	Bytes      int64
	Violations int64
}

// Limits is implemented by a backend that keeps quarantine and the counters of the day.
type Limits interface {
	// Quarantine puts a source in quarantine; fresh is false if it was in already (nothing changes).
	Quarantine(info QuarantineInfo) (fresh bool, err error)
	// ReleaseSource takes a source out of quarantine (a fork included) and clears its breaches of today; false
	// if it was not in.
	ReleaseSource(origin string) (bool, error)
	// Quarantined lists the sources in quarantine, forks included.
	Quarantined() ([]QuarantineInfo, error)
	// DayStats: what is recorded of a source for a day (UTC, 2006-01-02).
	DayStats(origin, day string) (DayStats, error)
	// AddDayStats adds to the record of a source for a day.
	AddDayStats(origin, day string, d DayStats) error
}
