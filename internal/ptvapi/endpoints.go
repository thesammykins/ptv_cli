package ptvapi

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// expand option codes used by the departures/pattern endpoints.
const (
	ExpandAll        = "0"
	ExpandStop       = "1"
	ExpandRoute      = "2"
	ExpandRun        = "3"
	ExpandDirection  = "4"
	ExpandDisruption = "5"
)

// RouteTypes returns all transport modes and their names.
func (c *Client) RouteTypes(ctx context.Context) (*RouteTypesResponse, error) {
	var out RouteTypesResponse
	if err := c.get(ctx, "/v3/route_types", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Search returns stops, routes and outlets matching a term.
func (c *Client) Search(ctx context.Context, term string, routeTypes []int) (*SearchResult, error) {
	q := url.Values{}
	for _, rt := range routeTypes {
		q.Add("route_types", strconv.Itoa(rt))
	}
	// The search term lives in the URL path, which PTV signs. A "/" is a path
	// separator and makes the signature invalid (403), so collapse slashes to
	// spaces — many PTV stop names use "/" (e.g. "Bourke St/Spencer St").
	term = strings.ReplaceAll(term, "/", " ")
	term = strings.Join(strings.Fields(term), " ")
	var out SearchResult
	if err := c.get(ctx, "/v3/search/"+url.PathEscape(term), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Routes lists routes, optionally filtered by mode and/or name.
func (c *Client) Routes(ctx context.Context, routeTypes []int, name string) (*RouteResponse, error) {
	q := url.Values{}
	for _, rt := range routeTypes {
		q.Add("route_types", strconv.Itoa(rt))
	}
	if name != "" {
		q.Set("route_name", name)
	}
	var out RouteResponse
	if err := c.get(ctx, "/v3/routes", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Route returns a single route by id.
func (c *Client) Route(ctx context.Context, routeID int) (*RouteResponse, error) {
	var out RouteResponse
	if err := c.get(ctx, fmt.Sprintf("/v3/routes/%d", routeID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Directions returns the directions a route travels in.
func (c *Client) Directions(ctx context.Context, routeID int) (*DirectionsResponse, error) {
	var out DirectionsResponse
	if err := c.get(ctx, fmt.Sprintf("/v3/directions/route/%d", routeID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StopsForRoute returns all stops on a route (for a given mode).
func (c *Client) StopsForRoute(ctx context.Context, routeID, routeType int, directionID *int) (*StopsOnRouteResponse, error) {
	q := url.Values{}
	if directionID != nil {
		q.Set("direction_id", strconv.Itoa(*directionID))
	}
	var out StopsOnRouteResponse
	path := fmt.Sprintf("/v3/stops/route/%d/route_type/%d", routeID, routeType)
	if err := c.get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StopsNearLocation returns stops near a lat/lng.
func (c *Client) StopsNearLocation(ctx context.Context, lat, lng float64, routeTypes []int, maxResults int, maxDistance float64) (*StopsByDistanceResponse, error) {
	q := url.Values{}
	for _, rt := range routeTypes {
		q.Add("route_types", strconv.Itoa(rt))
	}
	if maxResults > 0 {
		q.Set("max_results", strconv.Itoa(maxResults))
	}
	if maxDistance > 0 {
		q.Set("max_distance", strconv.FormatFloat(maxDistance, 'f', -1, 64))
	}
	var out StopsByDistanceResponse
	path := fmt.Sprintf("/v3/stops/location/%s,%s",
		strconv.FormatFloat(lat, 'f', -1, 64),
		strconv.FormatFloat(lng, 'f', -1, 64))
	if err := c.get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StopDetails returns facility/platform information for a stop.
func (c *Client) StopDetails(ctx context.Context, stopID, routeType int) (*StopResponse, error) {
	q := url.Values{}
	q.Set("stop_location", "true")
	q.Set("stop_amenities", "true")
	q.Set("stop_accessibility", "true")
	q.Set("stop_staffing", "true")
	q.Set("stop_disruptions", "true")
	var out StopResponse
	path := fmt.Sprintf("/v3/stops/%d/route_type/%d", stopID, routeType)
	if err := c.get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeparturesOptions controls a departures query.
type DeparturesOptions struct {
	RouteID     int // 0 = all routes
	DirectionID *int
	MaxResults  int
	Expand      []string
	DateUTC     string
}

// Departures returns real-time/timetabled departures from a stop.
func (c *Client) Departures(ctx context.Context, routeType, stopID int, opts DeparturesOptions) (*DeparturesResponse, error) {
	q := url.Values{}
	if opts.MaxResults > 0 {
		q.Set("max_results", strconv.Itoa(opts.MaxResults))
	}
	if opts.DirectionID != nil {
		q.Set("direction_id", strconv.Itoa(*opts.DirectionID))
	}
	if opts.DateUTC != "" {
		q.Set("date_utc", opts.DateUTC)
	}
	for _, e := range opts.Expand {
		q.Add("expand", e)
	}

	path := fmt.Sprintf("/v3/departures/route_type/%d/stop/%d", routeType, stopID)
	if opts.RouteID > 0 {
		path = fmt.Sprintf("%s/route/%d", path, opts.RouteID)
	}
	var out DeparturesResponse
	if err := c.get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DisruptionsAll returns all current/planned disruptions, optionally filtered by mode.
func (c *Client) DisruptionsAll(ctx context.Context, routeTypes []int) (*DisruptionsResponse, error) {
	q := url.Values{}
	for _, rt := range routeTypes {
		q.Add("route_types", strconv.Itoa(rt))
	}
	var out DisruptionsResponse
	if err := c.get(ctx, "/v3/disruptions", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DisruptionsForRoute returns disruptions for a specific route.
func (c *Client) DisruptionsForRoute(ctx context.Context, routeID int) (*DisruptionsResponse, error) {
	var out DisruptionsResponse
	if err := c.get(ctx, fmt.Sprintf("/v3/disruptions/route/%d", routeID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DisruptionsForStop returns disruptions for a specific stop.
func (c *Client) DisruptionsForStop(ctx context.Context, stopID int) (*DisruptionsResponse, error) {
	var out DisruptionsResponse
	if err := c.get(ctx, fmt.Sprintf("/v3/disruptions/stop/%d", stopID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FareEstimate estimates a fare between zones.
func (c *Client) FareEstimate(ctx context.Context, minZone, maxZone int) (*FareEstimateResponse, error) {
	var out FareEstimateResponse
	path := fmt.Sprintf("/v3/fare_estimate/min_zone/%d/max_zone/%d", minZone, maxZone)
	if err := c.get(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Outlets lists myki ticket outlets.
func (c *Client) Outlets(ctx context.Context, maxResults int) (*OutletResponse, error) {
	q := url.Values{}
	if maxResults > 0 {
		q.Set("max_results", strconv.Itoa(maxResults))
	}
	var out OutletResponse
	if err := c.get(ctx, "/v3/outlets", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
