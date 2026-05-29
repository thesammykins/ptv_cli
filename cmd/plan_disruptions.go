package cmd

import (
	"context"
	"sort"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/model"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

// annotateDisruptions overlays current/planned PTV disruptions onto a planned
// journey, flagging affected transit legs and collecting a journey-level
// summary. It is best-effort: any error (offline, no credentials, API failure)
// is returned for the caller to surface as a non-fatal note.
func annotateDisruptions(ctx context.Context, client *ptvapi.Client, j *model.Journey) error {
	// Determine which API route_types the journey touches.
	typeSet := map[int]bool{}
	for _, l := range j.Legs {
		if l.Walk {
			continue
		}
		if rt := feedToAPIType(l.RouteType); rt >= 0 {
			typeSet[rt] = true
		}
	}
	var routeTypes []int
	for rt := range typeSet {
		routeTypes = append(routeTypes, rt)
	}

	resp, err := client.DisruptionsAll(ctx, routeTypes)
	if err != nil {
		return err
	}

	// Index disruptions by a normalized route key ("name"/"number"), keeping
	// the API route_type to disambiguate same-named routes across modes.
	type keyed struct {
		rt  int
		dis ptvapi.Disruption
	}
	byKey := map[string][]keyed{}
	for _, list := range resp.Disruptions {
		for _, d := range list {
			for _, r := range d.Routes {
				for _, k := range routeKeys(r.RouteName, r.RouteNumber) {
					byKey[k] = append(byKey[k], keyed{rt: r.RouteType, dis: d})
				}
			}
		}
	}

	notes := map[int64]model.DisruptionNote{}
	for i := range j.Legs {
		l := &j.Legs[i]
		if l.Walk {
			continue
		}
		apiType := feedToAPIType(l.RouteType)
		seen := map[int64]bool{}
		for _, k := range routeKeys(l.RouteShortName, l.RouteLongName) {
			for _, cand := range byKey[k] {
				if apiType >= 0 && cand.rt >= 0 && cand.rt != apiType {
					continue
				}
				id := cand.dis.DisruptionID
				if seen[id] {
					continue
				}
				seen[id] = true
				l.Disrupted = true
				l.DisruptionIDs = append(l.DisruptionIDs, id)
				notes[id] = model.DisruptionNote{
					ID:     id,
					Title:  cand.dis.Title,
					Status: cand.dis.DisruptionStatus,
					Type:   cand.dis.DisruptionType,
					URL:    cand.dis.URL,
				}
			}
		}
		sort.Slice(l.DisruptionIDs, func(a, b int) bool { return l.DisruptionIDs[a] < l.DisruptionIDs[b] })
	}

	for _, n := range notes {
		j.Disruptions = append(j.Disruptions, n)
	}
	sort.Slice(j.Disruptions, func(a, b int) bool { return j.Disruptions[a].ID < j.Disruptions[b].ID })
	return nil
}

// routeKeys returns the normalized lookup keys for a route, derived from its
// name and number/short-name. Empty values are skipped.
func routeKeys(values ...string) []string {
	var out []string
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// feedToAPIType maps a PTV GTFS feed mode to the Timetable API route_type used
// by the disruptions endpoint. Returns -1 when there is no clean mapping.
func feedToAPIType(feedMode int) int {
	switch feedMode {
	case 2: // metropolitan train
		return 0
	case 3: // metropolitan tram
		return 1
	case 4, 6, 7, 8: // metropolitan / regional bus
		return 2
	case 1, 5: // V/Line train, V/Line coach
		return 3
	default:
		return -1
	}
}
