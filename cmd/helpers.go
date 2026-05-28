package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/elsammykins/ptv_cli/internal/ptvapi"
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
