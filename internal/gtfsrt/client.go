package gtfsrt

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// Vehicle is a normalized GTFS Realtime vehicle-position entity.
type Vehicle struct {
	EntityID        string
	TripID          string
	RouteID         string
	StartDate       string
	StartTime       string
	VehicleID       string
	Label           string
	LicensePlate    string
	StopID          string
	CurrentStatus   string
	OccupancyStatus string
	TimestampUTC    string
	Latitude        *float64
	Longitude       *float64
	Bearing         *float64
	Speed           *float64
}

// Client fetches Transport Victoria GTFS Realtime protobuf feeds.
type Client struct {
	keyID string
	apiID string
	http  *http.Client
}

// New constructs a GTFS Realtime client using Transport Victoria Open Data credentials.
func New(keyID, apiID string) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		keyID: keyID,
		apiID: apiID,
		http:  &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// Fetch fetches and decodes a GTFS Realtime protobuf feed.
func (c *Client) Fetch(ctx context.Context, feedURL string) (*gtfs.FeedMessage, error) {
	if c.keyID == "" {
		return c.fetch(ctx, feedURL, "")
	}
	errs := make([]string, 0, 3)
	for _, headerName := range []string{"Ocp-Apim-Subscription-Key", "KeyID", "KeyId"} {
		feed, err := c.fetch(ctx, feedURL, headerName)
		if err == nil {
			return feed, nil
		}
		errs = append(errs, err.Error())
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}

// FetchVehiclePositions fetches and decodes a GTFS Realtime vehicle positions feed.
func (c *Client) FetchVehiclePositions(ctx context.Context, feedURL string) (*gtfs.FeedMessage, error) {
	return c.Fetch(ctx, feedURL)
}

func (c *Client) fetch(ctx context.Context, feedURL, headerName string) (*gtfs.FeedMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/x-protobuf")
	if headerName != "" {
		req.Header[headerName] = []string{c.keyID}
	}
	if c.apiID != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(body))
		if message != "" && len(message) <= 300 {
			if headerName == "" {
				return nil, fmt.Errorf("unauthenticated request: GTFS Realtime API error (%d): %s", resp.StatusCode, message)
			}
			return nil, fmt.Errorf("%s header: GTFS Realtime API error (%d): %s", headerName, resp.StatusCode, message)
		}
		if headerName == "" {
			return nil, fmt.Errorf("unauthenticated request: GTFS Realtime API error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s header: GTFS Realtime API error (%d)", headerName, resp.StatusCode)
	}

	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decoding GTFS Realtime feed: %w", err)
	}
	return &feed, nil
}

// Vehicles normalizes all vehicle-position entities in a feed.
func Vehicles(feed *gtfs.FeedMessage) []Vehicle {
	if feed == nil {
		return nil
	}
	vehicles := make([]Vehicle, 0, len(feed.GetEntity()))
	for _, entity := range feed.GetEntity() {
		pos := entity.GetVehicle()
		if pos == nil {
			continue
		}
		trip := pos.GetTrip()
		desc := pos.GetVehicle()
		vehicle := Vehicle{
			EntityID:      entity.GetId(),
			TripID:        trip.GetTripId(),
			RouteID:       trip.GetRouteId(),
			StartDate:     trip.GetStartDate(),
			StartTime:     trip.GetStartTime(),
			VehicleID:     desc.GetId(),
			Label:         desc.GetLabel(),
			LicensePlate:  desc.GetLicensePlate(),
			StopID:        pos.GetStopId(),
			CurrentStatus: pos.GetCurrentStatus().String(),
		}
		if pos.OccupancyStatus != nil {
			vehicle.OccupancyStatus = pos.GetOccupancyStatus().String()
		}
		if ts := pos.GetTimestamp(); ts > 0 {
			vehicle.TimestampUTC = time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
		}
		if p := pos.GetPosition(); p != nil {
			lat := float64(p.GetLatitude())
			lng := float64(p.GetLongitude())
			bearing := float64(p.GetBearing())
			speed := float64(p.GetSpeed())
			vehicle.Latitude = &lat
			vehicle.Longitude = &lng
			vehicle.Bearing = &bearing
			vehicle.Speed = &speed
		}
		vehicles = append(vehicles, vehicle)
	}
	return vehicles
}

// FindByRunRef returns the first vehicle whose GTFS-R trip id matches a PTV run_ref.
func FindByRunRef(feed *gtfs.FeedMessage, runRef string) *Vehicle {
	runRef = normalize(runRef)
	if runRef == "" {
		return nil
	}
	for _, vehicle := range Vehicles(feed) {
		if normalize(vehicle.TripID) == runRef || normalize(vehicle.EntityID) == runRef {
			return &vehicle
		}
	}
	return nil
}

// FindByVehicleID returns the first vehicle whose public or internal vehicle id matches query.
func FindByVehicleID(feed *gtfs.FeedMessage, query string) *Vehicle {
	query = normalize(query)
	if query == "" {
		return nil
	}
	for _, vehicle := range Vehicles(feed) {
		for _, candidate := range []string{vehicle.VehicleID, vehicle.Label, vehicle.LicensePlate} {
			if vehicleIdentifierMatches(candidate, query) {
				return &vehicle
			}
		}
	}
	return nil
}

func vehicleIdentifierMatches(candidate, query string) bool {
	candidate = normalize(candidate)
	if candidate == "" || query == "" {
		return false
	}
	if candidate == query {
		return true
	}
	for _, part := range strings.FieldsFunc(candidate, func(r rune) bool {
		return r == '-' || r == ' ' || r == ',' || r == '/'
	}) {
		if part == query {
			return true
		}
	}
	return false
}

func normalize(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
