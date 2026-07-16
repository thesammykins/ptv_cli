package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	nextMode     string
	nextRoute    string
	nextPlatform string
)

var nextCmd = &cobra.Command{
	Use:   "next <stop-id|name>",
	Short: "Show how soon the next services depart a stop (real-time)",
	Long: `Show upcoming real-time departures from a stop, with countdowns,
scheduled vs estimated times, platform numbers and any disruptions.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := loadClient()
		if err != nil {
			return err
		}

		var modeHint []int
		if nextMode != "" {
			rt, ok := parseMode(nextMode)
			if !ok {
				return fmt.Errorf("unknown mode %q", nextMode)
			}
			modeHint = []int{rt}
		}

		stop, err := resolveStopContext(cmd.Context(), client, joinArgs(args), modeHint)
		if err != nil {
			return err
		}
		if stop.RouteType < 0 {
			return fmt.Errorf("a numeric stop id needs --mode (train, tram, bus, vline, nightbus)")
		}

		opts := ptvapi.DeparturesOptions{
			MaxResults: orDefault(flagLimit, 8),
			Expand:     []string{ptvapi.ExpandRoute, ptvapi.ExpandRun, ptvapi.ExpandDirection, ptvapi.ExpandDisruption},
		}
		if nextRoute != "" {
			routeTypes := modeHint
			if len(routeTypes) == 0 {
				routeTypes = []int{stop.RouteType}
			}
			route, rerr := resolveRouteWithTypesContext(cmd.Context(), client, nextRoute, routeTypes)
			if rerr != nil {
				return rerr
			}
			if rerr := ensureRouteServesStop(cmd.Context(), client, stop, route); rerr != nil {
				return rerr
			}
			opts.RouteID = route.RouteID
		}

		resp, err := client.Departures(cmd.Context(), stop.RouteType, stop.StopID, opts)
		if err != nil {
			return err
		}
		deps := resp.Departures
		if nextPlatform != "" {
			deps = filterPlatform(deps, nextPlatform)
		}
		sortDepartures(deps)
		deps = limitDepartures(deps)
		resp.Departures = deps
		if flagJSON {
			return printJSON(newNextOutput(resp))
		}

		stopName := stop.StopName
		if s, ok := resp.Stops[strconv.Itoa(stop.StopID)]; ok && s.StopName != "" {
			stopName = s.StopName
		} else if stopName == "" {
			stopName = fmt.Sprintf("Stop %d", stop.StopID)
		}
		fmt.Printf("Next departures — %s (%s)\n\n", render.CleanText(stopName), routeTypeName(stop.RouteType))

		if len(deps) == 0 {
			fmt.Println("No upcoming departures.")
			return nil
		}

		now := time.Now()
		t := render.NewTable("IN", "SCHEDULED", "EST", "PLAT", "ROUTE", "TOWARDS", "STATUS")
		for _, d := range deps {
			when, isEst := departureTime(d)
			countdown := "-"
			estStr := "-"
			schedStr := "-"
			if d.ScheduledDepartureUTC != nil {
				schedStr = formatLocal(*d.ScheduledDepartureUTC)
			}
			if d.EstimatedDepartureUTC != nil {
				estStr = formatLocal(*d.EstimatedDepartureUTC)
			}
			if !when.IsZero() {
				countdown = formatCountdown(when.Sub(now))
			}

			plat := "-"
			if d.PlatformNumber != nil && *d.PlatformNumber != "" {
				plat = *d.PlatformNumber
			}

			routeName := routeLabel(resp, d.RouteID)
			towards := destinationFor(resp, d)

			status := delayStatus(d, isEst)
			t.Row(countdown, schedStr, estStr, plat, routeName, towards, status)
		}
		if err := t.Flush(); err != nil {
			return err
		}

		// Surface any disruptions referenced by these departures.
		if len(resp.Disruptions) > 0 {
			printed := map[int64]bool{}
			first := true
			for _, d := range deps {
				for _, id := range d.DisruptionIDs {
					if printed[id] {
						continue
					}
					if dis, ok := resp.Disruptions[strconv.FormatInt(id, 10)]; ok {
						if first {
							fmt.Println("\nDisruptions")
							first = false
						}
						fmt.Printf("  • [%s] %s\n", render.CleanText(dis.DisruptionStatus), render.CleanText(dis.Title))
						printed[id] = true
					}
				}
			}
		}
		return nil
	},
}

// departureTime returns the best-known departure time (estimated preferred)
// and whether it was the estimate.
func departureTime(d ptvapi.Departure) (time.Time, bool) {
	if d.EstimatedDepartureUTC != nil {
		if t, err := time.Parse(time.RFC3339, *d.EstimatedDepartureUTC); err == nil {
			return localtime.InMelbourne(t), true
		}
	}
	if d.ScheduledDepartureUTC != nil {
		if t, err := time.Parse(time.RFC3339, *d.ScheduledDepartureUTC); err == nil {
			return localtime.InMelbourne(t), false
		}
	}
	return time.Time{}, false
}

// departureSort yields a sortable key (unix seconds) for a departure.
func departureSort(d ptvapi.Departure) int64 {
	t, _ := departureTime(d)
	if t.IsZero() {
		return 1<<62 - 1
	}
	return t.Unix()
}

// formatLocal parses a UTC ISO time and formats it explicitly in Melbourne.
func formatLocal(utc string) string {
	t, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		return "-"
	}
	return localtime.InMelbourne(t).Format("15:04 MST")
}

// formatCountdown renders a duration as a compact countdown.
func formatCountdown(d time.Duration) string {
	if d < 0 {
		return "now"
	}
	mins := int(d.Minutes())
	if mins < 1 {
		return "<1 min"
	}
	if mins < 60 {
		return fmt.Sprintf("%d min", mins)
	}
	return fmt.Sprintf("%dh %dm", mins/60, mins%60)
}

// delayStatus describes whether a service is on time / delayed / scheduled.
func delayStatus(d ptvapi.Departure, isEst bool) string {
	if !isEst || d.ScheduledDepartureUTC == nil || d.EstimatedDepartureUTC == nil {
		return "scheduled"
	}
	sched, err1 := time.Parse(time.RFC3339, *d.ScheduledDepartureUTC)
	est, err2 := time.Parse(time.RFC3339, *d.EstimatedDepartureUTC)
	if err1 != nil || err2 != nil {
		return "scheduled"
	}
	diff := est.Sub(sched).Round(time.Minute)
	switch {
	case diff <= 0 && diff > -time.Minute:
		return "on time"
	case diff > 0:
		return fmt.Sprintf("+%d min late", int(diff.Minutes()))
	default:
		return fmt.Sprintf("%d min early", int(diff.Minutes()))
	}
}

// routeLabel returns a human label for a route id using expanded route data.
func routeLabel(resp *ptvapi.DeparturesResponse, routeID int) string {
	if r, ok := resp.Routes[strconv.Itoa(routeID)]; ok {
		if r.RouteNumber != "" {
			return r.RouteNumber
		}
		if r.RouteName != "" {
			return r.RouteName
		}
	}
	return strconv.Itoa(routeID)
}

// ensureRouteServesStop distinguishes an invalid route/stop filter from a
// valid route that simply has no upcoming departures. StopsForRoute is the
// documented cross-mode route membership contract; Stop Details facilities
// are not documented for every mode.
func ensureRouteServesStop(ctx context.Context, client *ptvapi.Client, stop *ptvapi.StopModel, route *ptvapi.Route) error {
	stops, err := client.StopsForRoute(ctx, route.RouteID, route.RouteType, nil)
	if err != nil {
		return fmt.Errorf("checking whether route serves stop: %w", err)
	}
	for _, served := range stops.Stops {
		if served.StopID == stop.StopID {
			return nil
		}
	}

	routeName := normalizedText(route.RouteNumber)
	if routeName == "" {
		routeName = normalizedText(route.RouteName)
	}
	if routeName == "" {
		routeName = strconv.Itoa(route.RouteID)
	}
	stopName := normalizedText(stop.StopName)
	if stopName == "" {
		stopName = fmt.Sprintf("Stop %d", stop.StopID)
	}
	return fmt.Errorf(
		"route %q does not serve stop %q (%s); use 'ptv stops on %s --mode %s' to list its stops",
		routeName,
		stopName,
		strings.ToLower(routeTypeName(stop.RouteType)),
		routeName,
		strings.ToLower(routeTypeName(route.RouteType)),
	)
}

// destinationFor returns the destination/direction name for a departure.
func destinationFor(resp *ptvapi.DeparturesResponse, d ptvapi.Departure) string {
	if run, ok := resp.Runs[d.RunRef]; ok && run.DestinationName != "" {
		return run.DestinationName
	}
	if dir, ok := resp.Directions[strconv.Itoa(d.DirectionID)]; ok {
		return dir.DirectionName
	}
	return "-"
}

// filterPlatform keeps only departures from the given platform.
func filterPlatform(deps []ptvapi.Departure, platform string) []ptvapi.Departure {
	var out []ptvapi.Departure
	for _, d := range deps {
		if d.PlatformNumber != nil && *d.PlatformNumber == platform {
			out = append(out, d)
		}
	}
	return out
}

const commandTimeZone = "Australia/Melbourne"

// nextOutput preserves the Departures endpoint's top-level keys without
// exposing the upstream transport structs or vehicle-internal identifiers.
type nextOutput struct {
	Departures  []nextDepartureOutput          `json:"departures"`
	Stops       map[string]nextStopOutput      `json:"stops"`
	Routes      map[string]nextRouteOutput     `json:"routes"`
	Runs        map[string]nextRunOutput       `json:"runs"`
	Directions  map[string]nextDirectionOutput `json:"directions"`
	Disruptions map[string]disruptionOutput    `json:"disruptions"`
	Status      nextStatusOutput               `json:"status"`
	TimeZone    string                         `json:"time_zone"`
}

type nextDepartureOutput struct {
	StopID                int     `json:"stop_id"`
	PTVStopID             int     `json:"ptv_stop_id"`
	RouteID               int     `json:"route_id"`
	PTVRouteID            int     `json:"ptv_route_id"`
	RunID                 int     `json:"run_id"`
	RunRef                string  `json:"run_ref,omitempty"`
	PTVRunRef             string  `json:"ptv_run_ref,omitempty"`
	DirectionID           int     `json:"direction_id"`
	PTVDirectionID        int     `json:"ptv_direction_id"`
	DisruptionIDs         []int64 `json:"disruption_ids"`
	PTVDisruptionIDs      []int64 `json:"ptv_disruption_ids"`
	ScheduledDepartureUTC *string `json:"scheduled_departure_utc,omitempty"`
	EstimatedDepartureUTC *string `json:"estimated_departure_utc,omitempty"`
	ScheduledDeparture    *string `json:"scheduled_departure,omitempty"`
	EstimatedDeparture    *string `json:"estimated_departure,omitempty"`
	AtPlatform            bool    `json:"at_platform"`
	PlatformNumber        *string `json:"platform_number"`
	Flags                 string  `json:"flags,omitempty"`
	DepartureSequence     int     `json:"departure_sequence"`
	DepartureNote         *string `json:"departure_note,omitempty"`
	RouteLabel            string  `json:"route_label,omitempty"`
	Towards               string  `json:"towards,omitempty"`
	ServiceStatus         string  `json:"service_status"`
}

type nextStopOutput struct {
	StopID        int     `json:"stop_id"`
	PTVStopID     int     `json:"ptv_stop_id"`
	StopName      string  `json:"stop_name"`
	StopSuburb    string  `json:"stop_suburb"`
	RouteType     int     `json:"route_type"`
	StopLatitude  float64 `json:"stop_latitude"`
	StopLongitude float64 `json:"stop_longitude"`
	StopLandmark  string  `json:"stop_landmark,omitempty"`
	StopDistance  float64 `json:"stop_distance,omitempty"`
	StopSequence  int     `json:"stop_sequence"`
}

type nextRouteOutput struct {
	RouteType   int    `json:"route_type"`
	RouteID     int    `json:"route_id"`
	PTVRouteID  int    `json:"ptv_route_id"`
	RouteName   string `json:"route_name"`
	RouteNumber string `json:"route_number"`
	RouteGTFSID string `json:"route_gtfs_id,omitempty"`
}

type nextRunOutput struct {
	RunID            int    `json:"run_id"`
	RunRef           string `json:"run_ref,omitempty"`
	PTVRunRef        string `json:"ptv_run_ref,omitempty"`
	RouteID          int    `json:"route_id"`
	PTVRouteID       int    `json:"ptv_route_id"`
	RouteType        int    `json:"route_type"`
	FinalStopID      int    `json:"final_stop_id"`
	PTVFinalStopID   int    `json:"ptv_final_stop_id"`
	DestinationName  string `json:"destination_name"`
	Status           string `json:"status,omitempty"`
	DirectionID      int    `json:"direction_id"`
	PTVDirectionID   int    `json:"ptv_direction_id"`
	RunSequence      int    `json:"run_sequence"`
	ExpressStopCount int    `json:"express_stop_count"`
	RunNote          string `json:"run_note,omitempty"`
}

type nextDirectionOutput struct {
	DirectionID               int    `json:"direction_id"`
	PTVDirectionID            int    `json:"ptv_direction_id"`
	DirectionName             string `json:"direction_name"`
	RouteDirectionDescription string `json:"route_direction_description,omitempty"`
	RouteID                   int    `json:"route_id"`
	PTVRouteID                int    `json:"ptv_route_id"`
	RouteType                 int    `json:"route_type"`
}

type nextStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newNextOutput(response *ptvapi.DeparturesResponse) nextOutput {
	output := nextOutput{
		Departures:  make([]nextDepartureOutput, 0, len(response.Departures)),
		Stops:       make(map[string]nextStopOutput, len(response.Stops)),
		Routes:      make(map[string]nextRouteOutput, len(response.Routes)),
		Runs:        make(map[string]nextRunOutput, len(response.Runs)),
		Directions:  make(map[string]nextDirectionOutput, len(response.Directions)),
		Disruptions: make(map[string]disruptionOutput, len(response.Disruptions)),
		Status: nextStatusOutput{
			Version: normalizedText(response.Status.Version),
			Health:  response.Status.Health,
		},
		TimeZone: commandTimeZone,
	}
	for _, departure := range response.Departures {
		_, estimated := departureTime(departure)
		output.Departures = append(output.Departures, nextDepartureOutput{
			StopID:                departure.StopID,
			PTVStopID:             departure.StopID,
			RouteID:               departure.RouteID,
			PTVRouteID:            departure.RouteID,
			RunID:                 departure.RunID,
			RunRef:                normalizedText(departure.RunRef),
			PTVRunRef:             normalizedText(departure.RunRef),
			DirectionID:           departure.DirectionID,
			PTVDirectionID:        departure.DirectionID,
			DisruptionIDs:         append([]int64{}, departure.DisruptionIDs...),
			PTVDisruptionIDs:      append([]int64{}, departure.DisruptionIDs...),
			ScheduledDepartureUTC: normalizedUTCTime(departure.ScheduledDepartureUTC),
			EstimatedDepartureUTC: normalizedUTCTime(departure.EstimatedDepartureUTC),
			ScheduledDeparture:    normalizedMelbourneTime(departure.ScheduledDepartureUTC),
			EstimatedDeparture:    normalizedMelbourneTime(departure.EstimatedDepartureUTC),
			AtPlatform:            departure.AtPlatform,
			PlatformNumber:        normalizedString(departure.PlatformNumber),
			Flags:                 normalizedText(departure.Flags),
			DepartureSequence:     departure.DepartureSequence,
			DepartureNote:         normalizedString(departure.DepartureNote),
			RouteLabel:            normalizedText(routeLabel(response, departure.RouteID)),
			Towards:               normalizedText(destinationFor(response, departure)),
			ServiceStatus:         delayStatus(departure, estimated),
		})
	}
	for key, stop := range response.Stops {
		output.Stops[key] = nextStopOutput{
			StopID: stop.StopID, PTVStopID: stop.StopID,
			StopName: normalizedText(stop.StopName), StopSuburb: normalizedText(stop.StopSuburb),
			RouteType: stop.RouteType, StopLatitude: stop.StopLatitude, StopLongitude: stop.StopLongitude,
			StopLandmark: normalizedText(stop.StopLandmark), StopDistance: stop.StopDistance, StopSequence: stop.StopSequence,
		}
	}
	for key, route := range response.Routes {
		output.Routes[key] = nextRouteOutput{
			RouteType: route.RouteType, RouteID: route.RouteID, PTVRouteID: route.RouteID,
			RouteName: normalizedText(route.RouteName), RouteNumber: normalizedText(route.RouteNumber),
			RouteGTFSID: normalizedText(route.RouteGTFSID),
		}
	}
	for key, run := range response.Runs {
		output.Runs[key] = nextRunOutput{
			RunID: run.RunID, RunRef: normalizedText(run.RunRef), PTVRunRef: normalizedText(run.RunRef),
			RouteID: run.RouteID, PTVRouteID: run.RouteID, RouteType: run.RouteType,
			FinalStopID: run.FinalStopID, PTVFinalStopID: run.FinalStopID,
			DestinationName: normalizedText(run.DestinationName), Status: normalizedText(run.Status),
			DirectionID: run.DirectionID, PTVDirectionID: run.DirectionID,
			RunSequence: run.RunSequence, ExpressStopCount: run.ExpressStopCount, RunNote: normalizedText(run.RunNote),
		}
	}
	for key, direction := range response.Directions {
		output.Directions[key] = nextDirectionOutput{
			DirectionID: direction.DirectionID, PTVDirectionID: direction.DirectionID,
			DirectionName:             normalizedText(direction.DirectionName),
			RouteDirectionDescription: normalizedText(direction.RouteDirectionDescription),
			RouteID:                   direction.RouteID, PTVRouteID: direction.RouteID, RouteType: direction.RouteType,
		}
	}
	for key, disruption := range response.Disruptions {
		output.Disruptions[key] = newDisruptionOutput(disruption)
	}
	return output
}

func normalizedMelbourneTime(value *string) *string {
	if value == nil {
		return nil
	}
	cleaned := normalizedText(*value)
	parsed, err := time.Parse(time.RFC3339, cleaned)
	if err != nil {
		return nil
	}
	formatted := localtime.InMelbourne(parsed).Format(time.RFC3339)
	return &formatted
}

func normalizedUTCTime(value *string) *string {
	if value == nil {
		return nil
	}
	cleaned := normalizedText(*value)
	parsed, err := time.Parse(time.RFC3339, cleaned)
	if err != nil {
		return nil
	}
	formatted := parsed.UTC().Format(time.RFC3339)
	return &formatted
}

func sortDepartures(departures []ptvapi.Departure) {
	sort.SliceStable(departures, func(i, j int) bool {
		left, right := departureSort(departures[i]), departureSort(departures[j])
		if left != right {
			return left < right
		}
		if departures[i].RouteID != departures[j].RouteID {
			return departures[i].RouteID < departures[j].RouteID
		}
		if departures[i].RunRef != departures[j].RunRef {
			return departures[i].RunRef < departures[j].RunRef
		}
		if departures[i].DirectionID != departures[j].DirectionID {
			return departures[i].DirectionID < departures[j].DirectionID
		}
		if departures[i].DepartureSequence != departures[j].DepartureSequence {
			return departures[i].DepartureSequence < departures[j].DepartureSequence
		}
		if departures[i].StopID != departures[j].StopID {
			return departures[i].StopID < departures[j].StopID
		}
		if departures[i].RunID != departures[j].RunID {
			return departures[i].RunID < departures[j].RunID
		}
		if pointerText(departures[i].ScheduledDepartureUTC) != pointerText(departures[j].ScheduledDepartureUTC) {
			return pointerText(departures[i].ScheduledDepartureUTC) < pointerText(departures[j].ScheduledDepartureUTC)
		}
		if pointerText(departures[i].EstimatedDepartureUTC) != pointerText(departures[j].EstimatedDepartureUTC) {
			return pointerText(departures[i].EstimatedDepartureUTC) < pointerText(departures[j].EstimatedDepartureUTC)
		}
		if pointerText(departures[i].PlatformNumber) != pointerText(departures[j].PlatformNumber) {
			return pointerText(departures[i].PlatformNumber) < pointerText(departures[j].PlatformNumber)
		}
		if departures[i].Flags != departures[j].Flags {
			return departures[i].Flags < departures[j].Flags
		}
		if departures[i].AtPlatform != departures[j].AtPlatform {
			return !departures[i].AtPlatform
		}
		return pointerText(departures[i].DepartureNote) < pointerText(departures[j].DepartureNote)
	})
}

func pointerText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func resolveStopContext(ctx context.Context, client *ptvapi.Client, query string, modeHint []int) (*ptvapi.StopModel, error) {
	query = strings.TrimSpace(query)
	if id, err := strconv.Atoi(query); err == nil {
		stop := &ptvapi.StopModel{StopID: id, RouteType: -1}
		if len(modeHint) == 1 {
			stop.RouteType = modeHint[0]
		}
		return stop, nil
	}
	response, err := client.Search(ctx, query, modeHint)
	if err != nil {
		return nil, err
	}
	if len(response.Stops) == 0 {
		return nil, fmt.Errorf("no stop matching %q", query)
	}
	return chooseStop(query, response.Stops), nil
}

// orDefault returns v if positive, else def.
func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func init() {
	nextCmd.Flags().StringVar(&nextMode, "mode", "", "mode hint when passing a numeric stop id")
	nextCmd.Flags().StringVar(&nextRoute, "route", "", "filter to a specific route id or name")
	nextCmd.Flags().StringVar(&nextPlatform, "platform", "", "filter to a specific platform number")
	rootCmd.AddCommand(nextCmd)
}
