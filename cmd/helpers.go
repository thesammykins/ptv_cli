package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	cleanJSONStrings(reflect.ValueOf(v))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cleanJSONStrings(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		cleanJSONStrings(v.Elem())
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			cleanJSONStrings(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			cleanJSONStrings(v.Index(i))
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			item := v.MapIndex(key)
			if !item.IsValid() {
				continue
			}
			copy := reflect.New(item.Type()).Elem()
			copy.Set(item)
			cleanJSONStrings(copy)
			v.SetMapIndex(key, copy)
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(strings.TrimSpace(v.String()))
		}
	}
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

func limitRoutes(routes []ptvapi.Route) []ptvapi.Route {
	if flagLimit > 0 && len(routes) > flagLimit {
		return routes[:flagLimit]
	}
	return routes
}

func limitSearchResult(resp *ptvapi.SearchResult) {
	if resp == nil || flagLimit <= 0 {
		return
	}
	remaining := flagLimit
	if len(resp.Stops) > remaining {
		resp.Stops = resp.Stops[:remaining]
		resp.Routes = []ptvapi.Route{}
		resp.Outlets = []ptvapi.ResultOutlet{}
		return
	}
	remaining -= len(resp.Stops)
	if len(resp.Routes) > remaining {
		resp.Routes = resp.Routes[:remaining]
		resp.Outlets = []ptvapi.ResultOutlet{}
		return
	}
	remaining -= len(resp.Routes)
	if len(resp.Outlets) > remaining {
		resp.Outlets = resp.Outlets[:remaining]
	}
}
