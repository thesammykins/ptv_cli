package cmd

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var searchModes []string

var searchCmd = &cobra.Command{
	Use:   "search <term>",
	Short: "Search stops, routes and outlets",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		term := joinArgs(args)

		routeTypes, err := modesToTypes(searchModes)
		if err != nil {
			return err
		}

		resp, err := client.Search(cmd.Context(), term, routeTypes)
		if err != nil {
			return err
		}
		sortSearchResult(resp)
		limitSearchResult(resp)
		output := newSearchOutput(resp)
		if flagJSON {
			return printJSON(output)
		}

		if len(output.Stops) > 0 {
			fmt.Println("Stops")
			t := render.NewTable("ID", "NAME", "SUBURB", "MODE")
			for _, s := range output.Stops {
				t.Row(s.StopID, s.StopName, s.StopSuburb, routeTypeName(s.RouteType))
			}
			if err := t.Flush(); err != nil {
				return err
			}
			fmt.Println()
		}
		if len(output.Routes) > 0 {
			fmt.Println("Routes")
			t := render.NewTable("ID", "NUMBER", "NAME", "MODE")
			for _, r := range output.Routes {
				t.Row(r.RouteID, r.RouteNumber, r.RouteName, routeTypeName(r.RouteType))
			}
			if err := t.Flush(); err != nil {
				return err
			}
			fmt.Println()
		}
		if len(output.Outlets) > 0 {
			fmt.Println("Outlets")
			t := render.NewTable("NAME", "BUSINESS", "SUBURB")
			for _, o := range output.Outlets {
				t.Row(o.OutletName, o.OutletBusiness, o.OutletSuburb)
			}
			if err := t.Flush(); err != nil {
				return err
			}
		}
		if len(output.Stops) == 0 && len(output.Routes) == 0 && len(output.Outlets) == 0 {
			fmt.Println("No results.")
		}
		return nil
	},
}

type searchOutput struct {
	Stops   []searchStopOutput   `json:"stops"`
	Routes  []searchRouteOutput  `json:"routes"`
	Outlets []searchOutletOutput `json:"outlets"`
	Status  searchStatusOutput   `json:"status"`
}

type searchStopOutput struct {
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
}

type searchRouteOutput struct {
	RouteType   int    `json:"route_type"`
	RouteID     int    `json:"route_id"`
	PTVRouteID  int    `json:"ptv_route_id"`
	RouteName   string `json:"route_name"`
	RouteNumber string `json:"route_number"`
	RouteGTFSID string `json:"route_gtfs_id,omitempty"`
}

// searchOutletOutput intentionally omits outlet_slid_spid: it is an
// upstream/internal lookup value and is not a useful public identity.
type searchOutletOutput struct {
	OutletName      string  `json:"outlet_name"`
	OutletBusiness  string  `json:"outlet_business"`
	OutletLatitude  float64 `json:"outlet_latitude"`
	OutletLongitude float64 `json:"outlet_longitude"`
	OutletSuburb    string  `json:"outlet_suburb"`
	OutletDistance  float64 `json:"outlet_distance,omitempty"`
}

type searchStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newSearchOutput(response *ptvapi.SearchResult) searchOutput {
	output := searchOutput{
		Stops:   make([]searchStopOutput, 0, len(response.Stops)),
		Routes:  make([]searchRouteOutput, 0, len(response.Routes)),
		Outlets: make([]searchOutletOutput, 0, len(response.Outlets)),
		Status: searchStatusOutput{
			Version: normalizedText(response.Status.Version),
			Health:  response.Status.Health,
		},
	}
	for _, stop := range response.Stops {
		output.Stops = append(output.Stops, searchStopOutput{
			StopID: stop.StopID, PTVStopID: stop.StopID,
			StopName: normalizedText(stop.StopName), StopSuburb: normalizedText(stop.StopSuburb),
			RouteType: stop.RouteType, StopLatitude: stop.StopLatitude, StopLongitude: stop.StopLongitude,
			StopLandmark: normalizedText(stop.StopLandmark), StopDistance: stop.StopDistance,
			StopSequence: stop.StopSequence,
		})
	}
	for _, route := range response.Routes {
		output.Routes = append(output.Routes, searchRouteOutput{
			RouteType: route.RouteType, RouteID: route.RouteID, PTVRouteID: route.RouteID,
			RouteName: normalizedText(route.RouteName), RouteNumber: normalizedText(route.RouteNumber),
			RouteGTFSID: normalizedText(route.RouteGTFSID),
		})
	}
	for _, outlet := range response.Outlets {
		output.Outlets = append(output.Outlets, searchOutletOutput{
			OutletName: normalizedText(outlet.OutletName), OutletBusiness: normalizedText(outlet.OutletBusiness),
			OutletLatitude: outlet.OutletLatitude, OutletLongitude: outlet.OutletLongitude,
			OutletSuburb: normalizedText(outlet.OutletSuburb), OutletDistance: outlet.OutletDistance,
		})
	}
	return output
}

func sortSearchResult(response *ptvapi.SearchResult) {
	sort.SliceStable(response.Stops, func(i, j int) bool {
		left, right := response.Stops[i], response.Stops[j]
		if left.RouteType != right.RouteType {
			return left.RouteType < right.RouteType
		}
		if normalizedText(left.StopName) != normalizedText(right.StopName) {
			return normalizedText(left.StopName) < normalizedText(right.StopName)
		}
		if normalizedText(left.StopSuburb) != normalizedText(right.StopSuburb) {
			return normalizedText(left.StopSuburb) < normalizedText(right.StopSuburb)
		}
		return left.StopID < right.StopID
	})
	sortLineRoutes(response.Routes)
	sort.SliceStable(response.Outlets, func(i, j int) bool {
		left, right := response.Outlets[i], response.Outlets[j]
		if normalizedText(left.OutletSuburb) != normalizedText(right.OutletSuburb) {
			return normalizedText(left.OutletSuburb) < normalizedText(right.OutletSuburb)
		}
		if normalizedText(left.OutletName) != normalizedText(right.OutletName) {
			return normalizedText(left.OutletName) < normalizedText(right.OutletName)
		}
		if normalizedText(left.OutletBusiness) != normalizedText(right.OutletBusiness) {
			return normalizedText(left.OutletBusiness) < normalizedText(right.OutletBusiness)
		}
		if left.OutletLatitude != right.OutletLatitude {
			return left.OutletLatitude < right.OutletLatitude
		}
		if left.OutletLongitude != right.OutletLongitude {
			return left.OutletLongitude < right.OutletLongitude
		}
		return left.OutletDistance < right.OutletDistance
	})
}

func init() {
	searchCmd.Flags().StringSliceVar(&searchModes, "mode", nil, "filter by mode(s): train,tram,bus,vline,nightbus")
	rootCmd.AddCommand(searchCmd)
}
