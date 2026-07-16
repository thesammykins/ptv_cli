// Package localtime centralizes Victorian public-transport time semantics.
package localtime

import (
	"time"
	_ "time/tzdata"
)

var melbourne = mustLoadMelbourne()

func mustLoadMelbourne() *time.Location {
	location, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		panic("loading embedded Australia/Melbourne timezone: " + err.Error())
	}
	return location
}

// Melbourne returns the shared Australia/Melbourne location.
func Melbourne() *time.Location { return melbourne }

// InMelbourne converts an instant for user-facing Victorian display.
func InMelbourne(value time.Time) time.Time { return value.In(melbourne) }

// ServiceDayAnchor implements GTFS's local-noon-minus-12-hours definition.
// Adding GTFS seconds to this instant remains correct across DST transitions.
func ServiceDayAnchor(day time.Time) time.Time {
	local := day.In(melbourne)
	noon := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, melbourne)
	return noon.Add(-12 * time.Hour)
}
