package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"
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

		stop, err := resolveStop(client, joinArgs(args), modeHint)
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
			route, rerr := resolveRouteWithTypes(client, nextRoute, modeHint)
			if rerr != nil {
				return rerr
			}
			opts.RouteID = route.RouteID
		}

		resp, err := client.Departures(ctx(), stop.RouteType, stop.StopID, opts)
		if err != nil {
			return err
		}
		deps := resp.Departures
		if nextPlatform != "" {
			deps = filterPlatform(deps, nextPlatform)
		}
		sort.Slice(deps, func(i, j int) bool {
			return departureSort(deps[i]) < departureSort(deps[j])
		})
		deps = limitDepartures(deps)
		resp.Departures = deps
		if flagJSON {
			return printJSON(resp)
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
			return t.Local(), true
		}
	}
	if d.ScheduledDepartureUTC != nil {
		if t, err := time.Parse(time.RFC3339, *d.ScheduledDepartureUTC); err == nil {
			return t.Local(), false
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

// formatLocal parses a UTC ISO time and formats it in local time as 15:04.
func formatLocal(utc string) string {
	t, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		return "-"
	}
	return t.Local().Format("15:04")
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
