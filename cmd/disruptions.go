package cmd

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	disruptionsModes []string
	disruptionsRoute string
)

var disruptionsCmd = &cobra.Command{
	Use:   "disruptions",
	Short: "View current and planned service disruptions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}
		routeTypes, terr := modesToTypes(disruptionsModes)
		if terr != nil {
			return terr
		}
		return runDisruptions(cmd.Context(), client, routeTypes, disruptionsRoute)
	},
}

func runDisruptions(ctx context.Context, client *ptvapi.Client, routeTypes []int, routeQuery string) error {
	var resp *ptvapi.DisruptionsResponse
	var err error
	if routeQuery != "" {
		route, rerr := resolveRouteWithTypesContext(ctx, client, routeQuery, routeTypes)
		if rerr != nil {
			return rerr
		}
		resp, err = client.DisruptionsForRoute(ctx, route.RouteID)
	} else {
		resp, err = client.DisruptionsAll(ctx, routeTypes)
	}
	if err != nil {
		return err
	}
	sortDisruptionMap(resp.Disruptions)
	resp.Disruptions = limitDisruptionMap(resp.Disruptions)
	output := newDisruptionsOutput(resp)
	if flagJSON {
		return printJSON(output)
	}

	modes := make([]string, 0, len(output.Disruptions))
	for m := range output.Disruptions {
		modes = append(modes, m)
	}
	sort.Strings(modes)

	total := 0
	for _, m := range modes {
		items := output.Disruptions[m]
		if len(items) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", render.CleanText(m))
		t := render.NewTable("STATUS", "TITLE")
		for _, d := range items {
			t.Row(d.DisruptionStatus, d.Title)
			total++
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	if total == 0 {
		fmt.Println("No disruptions.")
	}
	return nil
}

type disruptionsOutput struct {
	Disruptions map[string][]disruptionOutput `json:"disruptions"`
	Status      disruptionStatusOutput        `json:"status"`
	TimeZone    string                        `json:"time_zone"`
}

type disruptionOutput struct {
	DisruptionID     int64                   `json:"disruption_id"`
	PTVDisruptionID  int64                   `json:"ptv_disruption_id"`
	Title            string                  `json:"title"`
	URL              string                  `json:"url,omitempty"`
	Description      string                  `json:"description,omitempty"`
	DisruptionStatus string                  `json:"disruption_status,omitempty"`
	DisruptionType   string                  `json:"disruption_type,omitempty"`
	PublishedOn      string                  `json:"published_on,omitempty"`
	LastUpdated      string                  `json:"last_updated,omitempty"`
	FromDate         string                  `json:"from_date,omitempty"`
	ToDate           *string                 `json:"to_date,omitempty"`
	Routes           []disruptionRouteOutput `json:"routes"`
	Stops            []disruptionStopOutput  `json:"stops"`
}

type disruptionRouteOutput struct {
	RouteType   int                        `json:"route_type"`
	RouteID     int                        `json:"route_id"`
	PTVRouteID  int                        `json:"ptv_route_id"`
	RouteName   string                     `json:"route_name"`
	RouteNumber string                     `json:"route_number"`
	Direction   *disruptionDirectionOutput `json:"direction,omitempty"`
}

type disruptionDirectionOutput struct {
	DirectionID    int    `json:"direction_id"`
	PTVDirectionID int    `json:"ptv_direction_id"`
	DirectionName  string `json:"direction_name"`
}

type disruptionStopOutput struct {
	RouteType int    `json:"route_type"`
	StopID    int    `json:"stop_id"`
	PTVStopID int    `json:"ptv_stop_id"`
	StopName  string `json:"stop_name"`
}

type disruptionStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newDisruptionsOutput(response *ptvapi.DisruptionsResponse) disruptionsOutput {
	output := disruptionsOutput{
		Disruptions: make(map[string][]disruptionOutput, len(response.Disruptions)),
		Status: disruptionStatusOutput{
			Version: normalizedText(response.Status.Version),
			Health:  response.Status.Health,
		},
		TimeZone: commandTimeZone,
	}
	for mode, disruptions := range response.Disruptions {
		items := make([]disruptionOutput, 0, len(disruptions))
		for _, disruption := range disruptions {
			items = append(items, newDisruptionOutput(disruption))
		}
		output.Disruptions[normalizedText(mode)] = items
	}
	return output
}

func newDisruptionOutput(disruption ptvapi.Disruption) disruptionOutput {
	output := disruptionOutput{
		DisruptionID:     disruption.DisruptionID,
		PTVDisruptionID:  disruption.DisruptionID,
		Title:            normalizedText(disruption.Title),
		URL:              normalizedPublicURL(disruption.URL),
		Description:      normalizedText(disruption.Description),
		DisruptionStatus: normalizedText(disruption.DisruptionStatus),
		DisruptionType:   normalizedText(disruption.DisruptionType),
		PublishedOn:      normalizedTimestampValue(disruption.PublishedOn),
		LastUpdated:      normalizedTimestampValue(disruption.LastUpdated),
		FromDate:         normalizedTimestampValue(disruption.FromDate),
		ToDate:           normalizedMelbourneTime(disruption.ToDate),
		Routes:           make([]disruptionRouteOutput, 0, len(disruption.Routes)),
		Stops:            make([]disruptionStopOutput, 0, len(disruption.Stops)),
	}
	for _, route := range disruption.Routes {
		item := disruptionRouteOutput{
			RouteType: route.RouteType, RouteID: route.RouteID, PTVRouteID: route.RouteID,
			RouteName: normalizedText(route.RouteName), RouteNumber: normalizedText(route.RouteNumber),
		}
		if route.Direction != nil {
			item.Direction = &disruptionDirectionOutput{
				DirectionID: route.Direction.DirectionID, PTVDirectionID: route.Direction.DirectionID,
				DirectionName: normalizedText(route.Direction.DirectionName),
			}
		}
		output.Routes = append(output.Routes, item)
	}
	for _, stop := range disruption.Stops {
		output.Stops = append(output.Stops, disruptionStopOutput{
			RouteType: stop.RouteType, StopID: stop.StopID, PTVStopID: stop.StopID,
			StopName: normalizedText(stop.StopName),
		})
	}
	return output
}

func normalizedTimestampValue(value string) string {
	converted := normalizedMelbourneTime(&value)
	if converted == nil {
		return ""
	}
	return *converted
}

func normalizedPublicURL(value string) string {
	cleaned := normalizedText(value)
	if cleaned == "" {
		return ""
	}
	parsed, err := url.Parse(cleaned)
	if err != nil || parsed.Host == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func sortDisruptionMap(disruptions map[string][]ptvapi.Disruption) {
	for mode := range disruptions {
		sort.SliceStable(disruptions[mode], func(i, j int) bool {
			left, right := disruptions[mode][i], disruptions[mode][j]
			if left.FromDate != right.FromDate {
				return left.FromDate < right.FromDate
			}
			if normalizedText(left.Title) != normalizedText(right.Title) {
				return normalizedText(left.Title) < normalizedText(right.Title)
			}
			return left.DisruptionID < right.DisruptionID
		})
	}
}

func init() {
	disruptionsCmd.Flags().StringSliceVar(&disruptionsModes, "mode", nil, "filter by mode(s)")
	disruptionsCmd.Flags().StringVar(&disruptionsRoute, "route", "", "filter by route id or name")
	rootCmd.AddCommand(disruptionsCmd)
}
