package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/geocode"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	stopsModes       []string
	stopsMaxDistance float64
)

var stopsCmd = &cobra.Command{
	Use:   "stops",
	Short: "Find stops near a location or on a route",
}

var stopsNearCmd = &cobra.Command{
	Use:   "near <lat,lng|place>",
	Short: "List stops near coordinates, a place or an address",
	Long: `List stops near coordinates, a place or an address.

Note: a coordinate beginning with '-' (Melbourne latitudes do) must follow a
'--' separator so it is not mistaken for a flag. Put any flags before '--'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sources, err := resolveSources(cmd.Context())
		if err != nil {
			return err
		}
		defer closeSources(sources)
		cfg := sources.Runtime
		lat, lng, err := parseLatLng(args[0])
		attribution := ""
		if err != nil {
			geocoder, gerr := geocode.NewWithOptions(geocode.Options{
				Endpoint:    cfg.GeocoderURL,
				Provider:    cfg.GeocoderProvider,
				Attribution: cfg.GeocoderAttribution,
				CacheDir:    filepath.Join(cfg.DataDir, "geocode"),
				BeforeRequest: func(_ string) {
					fmt.Fprintf(os.Stderr, "No coordinates supplied; sending the place query to %s.\n", render.CleanText(cfg.GeocoderProvider))
				},
			})
			if gerr != nil {
				return gerr
			}
			place, gerr := geocoder.Lookup(cmd.Context(), args[0])
			if gerr != nil {
				return fmt.Errorf("expected lat,lng or geocodable place: %w", gerr)
			}
			lat, lng = place.Lat, place.Lon
			attribution = place.Attribution
		}
		routeTypes, err := modesToTypes(stopsModes)
		if err != nil {
			return err
		}
		nearby, err := sources.GTFSStore.NearbyStops(cmd.Context(), lat, lng, gtfsFeedModes(routeTypes), stopsNearDistance(), flagLimit)
		if err != nil {
			return err
		}
		output := newGTFSStopsOutput(cmd.Context(), sources.GTFSStore, nearby, attribution, sources.GTFSFreshness)
		if flagJSON {
			return printJSON(output)
		}
		t := render.NewTable("ID", "STOP", "SUBURB", "MODE", "DIST(m)")
		for _, s := range output.Stops {
			t.Row(s.StopID, s.StopName, s.StopSuburb, routeTypeName(s.RouteType), fmt.Sprintf("%.0f", s.StopDistance))
		}
		if err := t.Flush(); err != nil {
			return err
		}
		if attribution != "" {
			fmt.Printf("\nData attribution: %s\n", render.CleanText(attribution))
		}
		return nil
	},
}

var stopsOnModes []string

var stopsOnCmd = &cobra.Command{
	Use:   "on <route-id|name>",
	Short: "List all stops on a route",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sources, err := resolveSources(cmd.Context())
		if err != nil {
			return err
		}
		defer closeSources(sources)
		routeTypes, err := modesToTypes(stopsOnModes)
		if err != nil {
			return err
		}
		route, err := resolveGTFSRoute(cmd.Context(), sources.GTFSStore, joinArgs(args), gtfsFeedModes(routeTypes))
		if err != nil {
			return err
		}
		stops, err := sources.GTFSStore.StopsOnRoute(cmd.Context(), route.RouteID, nil)
		if err != nil {
			return err
		}
		if flagLimit > 0 && len(stops) > flagLimit {
			stops = stops[:flagLimit]
		}
		output := newGTFSStopsOutput(cmd.Context(), sources.GTFSStore, func() []gtfs.NearbyStopResult {
			out := make([]gtfs.NearbyStopResult, len(stops))
			for i, stop := range stops {
				out[i] = gtfs.NearbyStopResult{StopResult: stop}
			}
			return out
		}(), "", sources.GTFSFreshness)
		if flagJSON {
			return printJSON(output)
		}
		fmt.Printf("Stops on %s (%s)\n", render.CleanText(route.LongName), gtfsModeName(route.FeedMode))
		t := render.NewTable("ID", "STOP", "SUBURB")
		for _, s := range output.Stops {
			t.Row(s.StopID, s.StopName, s.StopSuburb)
		}
		if err := t.Flush(); err != nil {
			return err
		}
		return nil
	},
}

type stopsOutput struct {
	Stops       []stopsStopOutput `json:"stops"`
	Status      stopsStatusOutput `json:"status"`
	Attribution string            `json:"attribution,omitempty"`
	DataSource  string            `json:"data_source,omitempty"`
	Freshness   *freshnessOutput  `json:"freshness,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
}

type stopsStopOutput struct {
	StopID        int     `json:"stop_id"`
	PTVStopID     int     `json:"ptv_stop_id"`
	StopName      string  `json:"stop_name"`
	StopSuburb    string  `json:"stop_suburb"`
	RouteType     int     `json:"route_type"`
	StopLatitude  float64 `json:"stop_latitude"`
	StopLongitude float64 `json:"stop_longitude"`
	StopLandmark  string  `json:"stop_landmark,omitempty"`
	StopDistance  float64 `json:"stop_distance,omitempty"`
	StopSequence  int     `json:"stop_sequence,omitempty"`
	GTFSStopID    string  `json:"gtfs_stop_id,omitempty"`
	Mode          string  `json:"mode,omitempty"`
}

type stopsStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newStopsOutput(stops []ptvapi.StopModel, status ptvapi.Status, attribution string) stopsOutput {
	output := stopsOutput{
		Stops:       make([]stopsStopOutput, 0, len(stops)),
		Status:      stopsStatusOutput{Version: normalizedText(status.Version), Health: status.Health},
		Attribution: normalizedText(attribution),
	}
	for _, stop := range stops {
		output.Stops = append(output.Stops, stopsStopOutput{
			StopID: stop.StopID, PTVStopID: stop.StopID,
			StopName: normalizedText(stop.StopName), StopSuburb: normalizedText(stop.StopSuburb),
			RouteType: stop.RouteType, StopLatitude: stop.StopLatitude, StopLongitude: stop.StopLongitude,
			StopLandmark: normalizedText(stop.StopLandmark), StopDistance: stop.StopDistance,
			StopSequence: stop.StopSequence,
		})
	}
	return output
}

// parseLatLng parses a "lat,lng" string.
func parseLatLng(s string) (float64, float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected lat,lng (e.g. -37.818,144.952)")
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid latitude: %w", err)
	}
	lng, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid longitude: %w", err)
	}
	return lat, lng, nil
}

func stopsNearDistance() float64 {
	if stopsMaxDistance > 0 {
		return stopsMaxDistance
	}
	return 1000
}

func init() {
	stopsNearCmd.Flags().StringSliceVar(&stopsModes, "mode", nil, "filter by mode(s)")
	stopsNearCmd.Flags().Float64Var(&stopsMaxDistance, "max-distance", 0, "max distance in metres (default 1000)")
	stopsOnCmd.Flags().StringSliceVar(&stopsOnModes, "mode", nil, "filter by mode(s)")
	stopsCmd.AddCommand(stopsNearCmd, stopsOnCmd)
	rootCmd.AddCommand(stopsCmd)
}
