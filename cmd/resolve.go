package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

// resolveRoute resolves a route from a numeric id or a (partial) name.
func resolveRoute(client *ptvapi.Client, query string) (*ptvapi.Route, error) {
	query = strings.TrimSpace(query)
	if id, err := strconv.Atoi(query); err == nil {
		resp, err := client.Route(ctx(), id)
		if err != nil {
			return nil, err
		}
		if resp.Route == nil {
			return nil, fmt.Errorf("no route with id %d", id)
		}
		return resp.Route, nil
	}

	resp, err := client.Routes(ctx(), nil, query)
	if err != nil {
		return nil, err
	}
	if len(resp.Routes) == 0 {
		return nil, fmt.Errorf("no route matching %q", query)
	}
	// Prefer an exact (case-insensitive) name match.
	for i := range resp.Routes {
		if strings.EqualFold(resp.Routes[i].RouteName, query) {
			return &resp.Routes[i], nil
		}
	}
	if len(resp.Routes) > 1 {
		var names []string
		for _, r := range resp.Routes {
			names = append(names, fmt.Sprintf("%s (%d)", r.RouteName, r.RouteID))
		}
		return nil, fmt.Errorf("%q is ambiguous; matches: %s", query, strings.Join(names, ", "))
	}
	return &resp.Routes[0], nil
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
