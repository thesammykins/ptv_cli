package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

// resolveRoute resolves a route from a numeric id, public route number, or name.
func resolveRoute(client *ptvapi.Client, query string) (*ptvapi.Route, error) {
	return resolveRouteWithTypes(client, query, nil)
}

func resolveRouteWithTypes(client *ptvapi.Client, query string, routeTypes []int) (*ptvapi.Route, error) {
	query = strings.TrimSpace(query)

	nameFilter := ""
	resp, err := client.Routes(ctx(), routeTypes, nameFilter)
	if err != nil {
		return nil, err
	}
	routes := resp.Routes

	// Human-facing route numbers are more useful than API route IDs when a mode
	// hint is present, e.g. tram 109 is route ID 722.
	if len(routeTypes) > 0 {
		for i := range routes {
			if strings.EqualFold(routes[i].RouteNumber, query) {
				return &routes[i], nil
			}
		}
	}

	if id, err := strconv.Atoi(query); err == nil {
		for i := range routes {
			if routes[i].RouteID == id {
				return &routes[i], nil
			}
		}
		if len(routeTypes) == 0 {
			resp, err := client.Route(ctx(), id)
			if err != nil {
				return nil, err
			}
			if resp.Route != nil {
				return resp.Route, nil
			}
		}
	}

	if len(resp.Routes) == 0 {
		return nil, fmt.Errorf("no route matching %q", query)
	}
	// Prefer an exact (case-insensitive) name match.
	for i := range routes {
		if strings.EqualFold(routes[i].RouteName, query) {
			return &routes[i], nil
		}
	}
	var contains []ptvapi.Route
	lower := strings.ToLower(query)
	for _, r := range routes {
		if strings.Contains(strings.ToLower(r.RouteName), lower) || strings.Contains(strings.ToLower(r.RouteNumber), lower) {
			contains = append(contains, r)
		}
	}
	if len(contains) == 0 {
		return nil, fmt.Errorf("no route matching %q", query)
	}
	routes = contains
	if len(routes) > 1 {
		var names []string
		for _, r := range routes {
			label := r.RouteName
			if r.RouteNumber != "" {
				label = r.RouteNumber + " " + label
			}
			names = append(names, fmt.Sprintf("%s (%d)", label, r.RouteID))
		}
		return nil, fmt.Errorf("%q is ambiguous; matches: %s", query, strings.Join(names, ", "))
	}
	return &routes[0], nil
}

// resolveStop resolves a stop from a numeric id, or a (partial) name via
// search. modeHint optionally narrows the search to a route_type.
func resolveStop(client *ptvapi.Client, query string, modeHint []int) (*ptvapi.StopModel, error) {
	query = strings.TrimSpace(query)
	if id, err := strconv.Atoi(query); err == nil {
		// A numeric id needs an accompanying mode to fetch details; return a
		// minimal stop carrying the id and (if provided) the mode hint.
		s := &ptvapi.StopModel{StopID: id}
		if len(modeHint) == 1 {
			s.RouteType = modeHint[0]
		} else {
			s.RouteType = -1
		}
		return s, nil
	}

	resp, err := client.Search(ctx(), query, modeHint)
	if err != nil {
		return nil, err
	}
	if len(resp.Stops) == 0 {
		return nil, fmt.Errorf("no stop matching %q", query)
	}
	for i := range resp.Stops {
		if strings.EqualFold(resp.Stops[i].StopName, query) {
			return &resp.Stops[i], nil
		}
	}
	return &resp.Stops[0], nil
}

func resolveStationStop(client *ptvapi.Client, query string, modeHint []int) (*ptvapi.StopModel, error) {
	if len(modeHint) == 0 {
		modeHint = []int{0}
	}
	stop, err := resolveStop(client, query, modeHint)
	if err == nil && stop.StopName != "" {
		return stop, nil
	}
	query = strings.TrimSpace(query)
	resp, err := client.Search(ctx(), query, modeHint)
	if err != nil {
		return nil, err
	}
	if len(resp.Stops) == 0 {
		if stop != nil {
			return stop, nil
		}
		return nil, fmt.Errorf("no station matching %q", query)
	}
	stationName := query
	if !strings.Contains(strings.ToLower(stationName), "station") {
		stationName += " Station"
	}
	for i := range resp.Stops {
		if strings.EqualFold(resp.Stops[i].StopName, stationName) {
			return &resp.Stops[i], nil
		}
	}
	for i := range resp.Stops {
		if strings.Contains(strings.ToLower(resp.Stops[i].StopName), " station") {
			return &resp.Stops[i], nil
		}
	}
	return &resp.Stops[0], nil
}
