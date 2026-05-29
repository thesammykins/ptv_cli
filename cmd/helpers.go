package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// routeTypeName maps a PTV route_type code to a human label.
func routeTypeName(rt int) string {
	switch rt {
	case 0:
		return "Train"
	case 1:
		return "Tram"
	case 2:
		return "Bus"
	case 3:
		return "V/Line"
	case 4:
		return "Night Bus"
	default:
		return fmt.Sprintf("Mode %d", rt)
	}
}

// parseMode converts a mode name to a PTV route_type code. Returns ok=false
// for an unknown name.
func parseMode(name string) (int, bool) {
	switch name {
	case "train", "metro", "metro_train":
		return 0, true
	case "tram":
		return 1, true
	case "bus":
		return 2, true
	case "vline", "v/line", "coach":
		return 3, true
	case "nightbus", "night_bus", "night-bus":
		return 4, true
	default:
		return 0, false
	}
}

// modesToTypes converts a list of mode names to route_type codes.
func modesToTypes(modes []string) ([]int, error) {
	var out []int
	for _, m := range modes {
		rt, ok := parseMode(strings.ToLower(strings.TrimSpace(m)))
		if !ok {
			return nil, fmt.Errorf("unknown mode %q (use train, tram, bus, vline, nightbus)", m)
		}
		out = append(out, rt)
	}
	return out, nil
}

// joinArgs joins positional args into a single space-separated term.
func joinArgs(args []string) string {
	return strings.TrimSpace(strings.Join(args, " "))
}

// limitStops applies the global --limit flag to a slice of stops.
func limitStops(stops []ptvapi.StopModel) []ptvapi.StopModel {
	if flagLimit > 0 && len(stops) > flagLimit {
		return stops[:flagLimit]
	}
	return stops
}

func sortStopsBySequence(stops []ptvapi.StopModel) {
	sort.Slice(stops, func(i, j int) bool {
		a, b := stops[i].StopSequence, stops[j].StopSequence
		if a == 0 && b != 0 {
			return false
		}
		if a != 0 && b == 0 {
			return true
		}
		if a != b {
			return a < b
		}
		return stops[i].StopName < stops[j].StopName
	})
}

func limitDepartures(deps []ptvapi.Departure) []ptvapi.Departure {
	if flagLimit > 0 && len(deps) > flagLimit {
		return deps[:flagLimit]
	}
	return deps
}

func limitOutlets(outlets []ptvapi.ResultOutlet) []ptvapi.ResultOutlet {
	if flagLimit > 0 && len(outlets) > flagLimit {
		return outlets[:flagLimit]
	}
	return outlets
}

func limitDisruptionMap(items map[string][]ptvapi.Disruption) map[string][]ptvapi.Disruption {
	if flagLimit <= 0 {
		return items
	}
	out := make(map[string][]ptvapi.Disruption, len(items))
	remaining := flagLimit
	modes := make([]string, 0, len(items))
	for mode := range items {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	for _, mode := range modes {
		list := items[mode]
		if remaining <= 0 {
			out[mode] = []ptvapi.Disruption{}
			continue
		}
		if len(list) > remaining {
			out[mode] = list[:remaining]
			remaining = 0
		} else {
			out[mode] = list
			remaining -= len(list)
		}
	}
	return out
}
