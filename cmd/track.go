package cmd

import (
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

var trackDate string

type trackOutput struct {
	Trip        *gtfs.TripDetailResult `json:"trip"`
	Realtime    trackRealtimeOutput    `json:"realtime"`
	Stops       []trackStopOutput      `json:"stops"`
	Vehicle     *trackVehicleOutput    `json:"vehicle,omitempty"`
	ServiceDate string                 `json:"service_date"`
	DataSource  string                 `json:"data_source"`
	Freshness   freshnessOutput        `json:"freshness"`
	Warnings    []string               `json:"warnings"`
}
type trackRealtimeOutput struct {
	State                string `json:"state"`
	ScheduleRelationship string `json:"schedule_relationship,omitempty"`
	MatchStrategy        string `json:"match_strategy"`
}
type trackStopOutput struct {
	StopID               string  `json:"stop_id"`
	StopName             string  `json:"stop_name"`
	StopSequence         int     `json:"stop_sequence"`
	ScheduledTime        string  `json:"scheduled_time"`
	EstimatedTime        *string `json:"estimated_time,omitempty"`
	DelaySeconds         *int32  `json:"delay_seconds,omitempty"`
	ScheduleRelationship string  `json:"schedule_relationship"`
}
type trackVehicleOutput struct {
	Label     string           `json:"label,omitempty"`
	Latitude  *float64         `json:"latitude,omitempty"`
	Longitude *float64         `json:"longitude,omitempty"`
	Bearing   *float64         `json:"bearing,omitempty"`
	Speed     *float64         `json:"speed,omitempty"`
	Freshness *sourceFreshness `json:"freshness,omitempty"`
}

var trackCmd = &cobra.Command{Use: "track <trip-id>", Short: "Track a scheduled trip with Open Data realtime updates", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	sources, err := resolveSources(cmd.Context())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	date, err := parseServiceDate(trackDate)
	if err != nil {
		return err
	}
	trip, err := sources.GTFSStore.TripDetail(cmd.Context(), strings.TrimSpace(args[0]), date)
	if err != nil {
		return err
	}
	output := trackOutput{Trip: trip, Realtime: trackRealtimeOutput{State: "unknown", MatchStrategy: realtimeJoinStrategy}, Stops: []trackStopOutput{}, ServiceDate: date.Format("20060102"), DataSource: "gtfs_static", Freshness: currentGTFSFreshness(cmd.Context(), sources.GTFSStore), Warnings: []string{}}
	anchor := localtime.ServiceDayAnchor(date)
	var snapshot *gtfsrt.Snapshot
	cache := gtfsrt.NewInvocationCache()
	client := gtfsrt.New(sources.OpenDataKey)
	if sources.OpenDataKey != "" {
		if feed, ok := realtimeFeedForMode(trip.FeedMode); ok {
			snapshot, err = cache.GetOrFetch(cmd.Context(), client, feed)
			if err != nil {
				output.Warnings = append(output.Warnings, realtimeWarning(err))
				fmt.Fprintln(os.Stderr, output.Warnings[len(output.Warnings)-1])
			} else {
				output.DataSource = "gtfs_static+opendata_realtime"
				output.Freshness.OpenDataRealtime = sourceFreshnessFromSnapshot(snapshot)
			}
		}
	} else {
		output.Warnings = append(output.Warnings, realtimeWarning(nil))
		fmt.Fprintln(os.Stderr, output.Warnings[len(output.Warnings)-1])
	}
	sourceTrip, _ := staticSourceID(trip.TripID)
	if snapshot != nil {
		if update, ok := snapshot.FindTripUpdate(gtfsrt.StaticTripID(sourceTrip), output.ServiceDate); ok {
			output.Realtime.State = "matched"
			output.Realtime.ScheduleRelationship = update.ScheduleRelationship
			for _, stop := range trip.Stops {
				item := trackStopOutput{StopID: stop.StopID, StopName: stop.StopName, StopSequence: stop.StopSequence, ScheduledTime: anchor.Add(time.Duration(stop.DepartureSec) * time.Second).Format(time.RFC3339), ScheduleRelationship: "SCHEDULED"}
				for _, realtimeStop := range update.StopTimeUpdates {
					if realtimeStop.StopSequence != nil && int(*realtimeStop.StopSequence) != stop.StopSequence {
						continue
					}
					if realtimeStop.StopID != "" {
						sourceStop, _ := staticSourceID(stop.StopID)
						if sourceStop != string(realtimeStop.StopID) {
							continue
						}
					}
					if realtimeStop.ScheduleRelationship != "" {
						item.ScheduleRelationship = realtimeStop.ScheduleRelationship
					}
					if realtimeStop.DepartureTime != nil {
						value := formatTrackRealtimeTime(*realtimeStop.DepartureTime)
						item.EstimatedTime = &value
					} else if realtimeStop.DepartureDelay != nil {
						value := anchor.Add(time.Duration(stop.DepartureSec) * time.Second).Add(time.Duration(*realtimeStop.DepartureDelay) * time.Second).Format(time.RFC3339)
						item.EstimatedTime = &value
					}
					item.DelaySeconds = realtimeStop.DepartureDelay
					break
				}
				output.Stops = append(output.Stops, item)
			}
			if update.ScheduleRelationship == "CANCELED" {
				for i := range output.Stops {
					output.Stops[i].ScheduleRelationship = "CANCELED"
				}
			}
			for _, vehicle := range snapshot.Vehicles {
				if vehicle.TripID == gtfsrt.StaticTripID(sourceTrip) && vehicle.StartDate == output.ServiceDate {
					output.Vehicle = &trackVehicleOutput{Label: string(vehicle.Label), Latitude: vehicle.Latitude, Longitude: vehicle.Longitude, Bearing: vehicle.Bearing, Speed: vehicle.Speed, Freshness: sourceFreshnessFromSnapshot(snapshot)}
					break
				}
			}
			if output.Vehicle == nil {
				if vehicleFeed, feedOK := realtimeVehicleFeedForMode(trip.FeedMode); feedOK {
					if vehicleSnapshot, vehicleErr := cache.GetOrFetch(cmd.Context(), client, vehicleFeed); vehicleErr == nil {
						output.Freshness.OpenDataRealtime = sourceFreshnessFromSnapshot(vehicleSnapshot)
						for _, vehicle := range vehicleSnapshot.Vehicles {
							if vehicle.TripID != gtfsrt.StaticTripID(sourceTrip) || vehicle.StartDate != output.ServiceDate {
								continue
							}
							output.Vehicle = &trackVehicleOutput{Label: string(vehicle.Label), Latitude: vehicle.Latitude, Longitude: vehicle.Longitude, Bearing: vehicle.Bearing, Speed: vehicle.Speed, Freshness: sourceFreshnessFromSnapshot(vehicleSnapshot)}
							break
						}
					}
				}
			}
		} else {
			for _, stop := range trip.Stops {
				output.Stops = append(output.Stops, trackStopOutput{StopID: stop.StopID, StopName: stop.StopName, StopSequence: stop.StopSequence, ScheduledTime: anchor.Add(time.Duration(stop.DepartureSec) * time.Second).Format(time.RFC3339), ScheduleRelationship: "SCHEDULED"})
			}
			output.Warnings = append(output.Warnings, "static trip has no validated current realtime update; showing scheduled stopping pattern")
		}
	} else {
		for _, stop := range trip.Stops {
			output.Stops = append(output.Stops, trackStopOutput{StopID: stop.StopID, StopName: stop.StopName, StopSequence: stop.StopSequence, ScheduledTime: anchor.Add(time.Duration(stop.DepartureSec) * time.Second).Format(time.RFC3339), ScheduleRelationship: "SCHEDULED"})
		}
		output.Warnings = append(output.Warnings, "static trip has no realtime match; showing scheduled stopping pattern")
	}
	if flagJSON {
		return printJSON(output)
	}
	fmt.Printf("Trip %s — realtime %s\n\n", render.CleanText(trip.TripID), output.Realtime.State)
	table := render.NewTable("SEQ", "STOP", "SCHEDULED", "ESTIMATED", "DELAY", "STATUS")
	for _, stop := range output.Stops {
		estimated := "-"
		if stop.EstimatedTime != nil {
			estimated = *stop.EstimatedTime
		}
		delay := "-"
		if stop.DelaySeconds != nil {
			delay = fmt.Sprintf("%ds", *stop.DelaySeconds)
		}
		table.Row(stop.StopSequence, stop.StopName, stop.ScheduledTime, estimated, delay, stop.ScheduleRelationship)
	}
	return table.Flush()
}}

func init() {
	trackCmd.Flags().StringVar(&trackDate, "date", "", "service date in YYYY-MM-DD (Melbourne time)")
	rootCmd.AddCommand(trackCmd)
}

func formatTrackRealtimeTime(unix int64) string {
	return time.Unix(unix, 0).In(localtime.Melbourne()).Format(time.RFC3339)
}
