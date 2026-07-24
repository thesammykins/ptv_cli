package cmd

import (
	"fmt"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
)

const realtimeJoinStrategy = "validated feed-local trip_id + start_date; namespace is explicit and never falls back"

func realtimeFeedForMode(mode int) (gtfsrt.Feed, bool) {
	name := ""
	switch mode {
	case 1, 5:
		name = "vline-trip-updates"
	case 2:
		name = "metro-trip-updates"
	case 3:
		name = "tram-trip-updates"
	case 4, 6:
		name = "bus-trip-updates"
	}
	if name == "" {
		return gtfsrt.Feed{}, false
	}
	return gtfsrt.FeedByID(name)
}

func realtimeVehicleFeedForMode(mode int) (gtfsrt.Feed, bool) {
	name := ""
	switch mode {
	case 1, 5:
		name = "vline-vehicle-positions"
	case 2:
		name = "metro-vehicle-positions"
	case 3:
		name = "tram-vehicle-positions"
	case 4, 6:
		name = "bus-vehicle-positions"
	}
	if name == "" {
		return gtfsrt.Feed{}, false
	}
	return gtfsrt.FeedByID(name)
}

func staticSourceID(public string) (string, bool) {
	_, source, ok := strings.Cut(strings.TrimSpace(public), ":")
	return source, ok && source != ""
}

func sourceFreshnessFromSnapshot(snapshot *gtfsrt.Snapshot) *sourceFreshness {
	if snapshot == nil {
		return &sourceFreshness{State: "unknown"}
	}
	fresh := snapshot.FeedFreshness
	result := &sourceFreshness{State: string(fresh.State)}
	if fresh.AgeSeconds != nil {
		result.AgeSeconds = float64(*fresh.AgeSeconds)
	}
	return result
}

func realtimeWarning(err error) string {
	if err == nil {
		return "real-time data unavailable; showing scheduled times only"
	}
	return fmt.Sprintf("real-time data unavailable; showing scheduled times only (%s)", err.Error())
}
