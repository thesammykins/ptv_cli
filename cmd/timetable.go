package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/gtfs"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/localtime"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	timetableRoute string
	timetableMode  string
	timetableDate  string
)

type timetableOutput struct {
	Departures  []timetableDepartureOutput `json:"departures"`
	ServiceDate string                     `json:"service_date"`
	DataSource  string                     `json:"data_source"`
	Freshness   freshnessOutput            `json:"freshness"`
	Warnings    []string                   `json:"warnings"`
}

type timetableDepartureOutput struct {
	gtfs.DepartureResult
	DepartureTime          string  `json:"departure_time"`
	ArrivalTime            string  `json:"arrival_time"`
	EstimatedDepartureTime *string `json:"estimated_departure_time,omitempty"`
	DelaySeconds           *int32  `json:"delay_seconds,omitempty"`
	ScheduleRelationship   string  `json:"schedule_relationship,omitempty"`
}

var timetableCmd = &cobra.Command{
	Use:   "timetable <stop-id|name>",
	Short: "Show scheduled departures for a stop",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sources, err := resolveSources(cmd.Context())
		if err != nil {
			return err
		}
		defer closeSources(sources)
		date, err := parseServiceDate(timetableDate)
		if err != nil {
			return err
		}
		modeTypes, err := modeNamesToTypes(timetableMode)
		if err != nil {
			return err
		}
		stop, err := resolveGTFSStop(cmd.Context(), sources.GTFSStore, strings.Join(args, " "), gtfsFeedModes(modeTypes))
		if err != nil {
			return err
		}
		departures, err := sources.GTFSStore.StopDepartures(cmd.Context(), stop.StopID, date, timetableRoute, gtfsFeedModes(modeTypes), flagLimit)
		if err != nil {
			return err
		}
		anchor := localtime.ServiceDayAnchor(date)
		output := timetableOutput{Departures: make([]timetableDepartureOutput, 0, len(departures)), ServiceDate: date.Format("20060102"), DataSource: "gtfs_static", Freshness: currentGTFSFreshness(cmd.Context(), sources.GTFSStore), Warnings: []string{}}
		for _, departure := range departures {
			output.Departures = append(output.Departures, timetableDepartureOutput{DepartureResult: departure, DepartureTime: anchor.Add(time.Duration(departure.DepartureSec) * time.Second).Format(time.RFC3339), ArrivalTime: anchor.Add(time.Duration(departure.ArrivalSec) * time.Second).Format(time.RFC3339)})
		}
		applyTimetableRealtime(cmd.Context(), sources, &output)
		if flagJSON {
			return printJSON(output)
		}
		fmt.Printf("Scheduled departures — %s (%s)\n\n", render.CleanText(stop.StopName), date.Format("2006-01-02"))
		t := render.NewTable("TIME", "ROUTE", "TOWARDS", "TRIP_ID")
		for _, departure := range output.Departures {
			t.Row(anchor.Add(time.Duration(departure.DepartureSec)*time.Second).Format("15:04 MST"), departure.RouteShortName, departure.Headsign, departure.TripID)
		}
		return t.Flush()
	},
}

func applyTimetableRealtime(ctx context.Context, sources *resolvedSources, output *timetableOutput) {
	if sources == nil || sources.OpenDataKey == "" || output == nil || len(output.Departures) == 0 {
		return
	}
	feed, ok := realtimeFeedForMode(output.Departures[0].FeedMode)
	if !ok {
		return
	}
	snapshot, err := gtfsrt.NewInvocationCache().GetOrFetch(ctx, gtfsrt.New(sources.OpenDataKey), feed)
	if err != nil {
		output.Warnings = append(output.Warnings, realtimeWarning(err))
		fmt.Fprintln(os.Stderr, output.Warnings[len(output.Warnings)-1])
		return
	}
	matched := 0
	for index := range output.Departures {
		staticTrip, ok := staticSourceID(output.Departures[index].TripID)
		if !ok {
			continue
		}
		update, ok := snapshot.FindTripUpdate(gtfsrt.StaticTripID(staticTrip), output.Departures[index].ServiceDate)
		if !ok {
			continue
		}
		output.Departures[index].ScheduleRelationship = update.ScheduleRelationship
		for _, stopUpdate := range update.StopTimeUpdates {
			if stopUpdate.StopSequence != nil && int(*stopUpdate.StopSequence) != output.Departures[index].StopSequence {
				continue
			}
			if stopUpdate.StopID != "" {
				staticStop, _ := staticSourceID(output.Departures[index].StopID)
				if staticStop != string(stopUpdate.StopID) {
					continue
				}
			}
			if stopUpdate.DepartureTime != nil {
				value := time.Unix(*stopUpdate.DepartureTime, 0).In(localtime.Melbourne()).Format(time.RFC3339)
				output.Departures[index].EstimatedDepartureTime = &value
			}
			output.Departures[index].DelaySeconds = stopUpdate.DepartureDelay
			matched++
			break
		}
	}
	if matched > 0 {
		output.DataSource = "gtfs_static+opendata_realtime"
		output.Freshness.OpenDataRealtime = sourceFreshnessFromSnapshot(snapshot)
	}
}

func parseServiceDate(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		now := time.Now().In(localtime.Melbourne())
		return time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, localtime.Melbourne()), nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(raw), localtime.Melbourne())
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid service date %q (want YYYY-MM-DD)", raw)
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 12, 0, 0, 0, localtime.Melbourne()), nil
}

func modeNamesToTypes(mode string) ([]int, error) {
	if strings.TrimSpace(mode) == "" {
		return nil, nil
	}
	return modesToTypes([]string{mode})
}

func init() {
	timetableCmd.Flags().StringVar(&timetableRoute, "route", "", "filter by route")
	timetableCmd.Flags().StringVar(&timetableMode, "mode", "", "filter by mode")
	timetableCmd.Flags().StringVar(&timetableDate, "date", "", "service date in YYYY-MM-DD (Melbourne time)")
	rootCmd.AddCommand(timetableCmd)
}
