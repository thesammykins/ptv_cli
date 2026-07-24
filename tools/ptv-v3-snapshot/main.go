// Command ptv-v3-snapshot generates the reviewed, credential-free v3
// enrichment snapshot embedded by the CLI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/atomicfile"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/v3static"
)

type stationSeed struct {
	RouteType int
	StopID    int
}

func main() {
	var envFile string
	var outputPath string
	var maxOutlets int
	var stationsFile string
	var migrateLegacy bool
	var stationSeeds stringList
	flag.StringVar(&envFile, "env-file", "", "explicit dotenv file containing PTV_API_KEY and PTV_API_USERID")
	flag.StringVar(&outputPath, "output", "internal/v3static/data/snapshot.json", "snapshot output path")
	flag.IntVar(&maxOutlets, "max-outlets", 0, "maximum outlet records (0 uses the API default)")
	flag.StringVar(&stationsFile, "stations-file", "data/ptv-v3/station-seeds.txt", "file of route_type:ptv_stop_id station seeds")
	flag.BoolVar(&migrateLegacy, "migrate-legacy", false, "rewrite an existing v1 snapshot without static route data; no credentials required")
	flag.Var(&stationSeeds, "station", "station seed in route_type:ptv_stop_id form; repeatable")
	flag.Parse()
	if migrateLegacy {
		if err := migrateLegacySnapshot(outputPath); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	fileSeeds, err := readStationSeeds(stationsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	stationSeeds = append(fileSeeds, stationSeeds...)
	if err := run(context.Background(), envFile, outputPath, maxOutlets, stationSeeds); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func readStationSeeds(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read station seeds: %w", err)
	}
	var values []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		values = append(values, line)
	}
	return values, nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func run(ctx context.Context, envFile, outputPath string, maxOutlets int, stationValues []string) error {
	credentials, err := config.LoadPTVCredentialsWithOptions(config.LoadOptions{EnvFile: envFile})
	if err != nil {
		return err
	}
	runtimeConfig, err := config.LoadRuntimeWithOptions(config.LoadOptions{EnvFile: envFile})
	if err != nil {
		return err
	}
	client := ptvapi.New(runtimeConfig.BaseURL, credentials.APIKey, credentials.DevID)

	outletsResponse, err := client.Outlets(ctx, maxOutlets)
	if err != nil {
		return fmt.Errorf("fetch outlets: %w", err)
	}

	snapshot := &v3static.Snapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Outlets:     make([]v3static.Outlet, 0, len(outletsResponse.Outlets)),
	}
	for _, outlet := range outletsResponse.Outlets {
		snapshot.Outlets = append(snapshot.Outlets, v3static.Outlet{
			OutletName: clean(outlet.OutletName), OutletBusiness: clean(outlet.OutletBusiness),
			OutletLatitude: outlet.OutletLatitude, OutletLongitude: outlet.OutletLongitude, OutletSuburb: clean(outlet.OutletSuburb),
		})
	}

	for _, value := range stationValues {
		seed, parseErr := parseStationSeed(value)
		if parseErr != nil {
			return parseErr
		}
		response, fetchErr := client.StopDetails(ctx, seed.StopID, seed.RouteType)
		if fetchErr != nil {
			return fmt.Errorf("fetch station %s: %w", value, fetchErr)
		}
		snapshot.StationFacilities = append(snapshot.StationFacilities, stationFacility(response))
	}

	if err := snapshot.RecomputeHash(); err != nil {
		return err
	}
	if existing, readErr := readSnapshot(outputPath); readErr == nil && existing.ContentHash == snapshot.ContentHash {
		fmt.Printf("unchanged content_sha256=%s outlets=%d station_facilities=%d\n", existing.ContentHash, len(existing.Outlets), len(existing.StationFacilities))
		return nil
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	if err := atomicfile.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("publish snapshot: %w", err)
	}
	fmt.Printf("updated content_sha256=%s outlets=%d station_facilities=%d\n", snapshot.ContentHash, len(snapshot.Outlets), len(snapshot.StationFacilities))
	return nil
}

func readSnapshot(path string) (*v3static.Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snapshot v3static.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func migrateLegacySnapshot(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read legacy snapshot: %w", err)
	}
	var snapshot v3static.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("decode legacy snapshot: %w", err)
	}
	if err := snapshot.RecomputeHash(); err != nil {
		return err
	}
	data, err = json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode migrated snapshot: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("publish migrated snapshot: %w", err)
	}
	fmt.Printf("migrated content_sha256=%s outlets=%d station_facilities=%d\n", snapshot.ContentHash, len(snapshot.Outlets), len(snapshot.StationFacilities))
	return nil
}

func parseStationSeed(value string) (stationSeed, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return stationSeed{}, fmt.Errorf("invalid station %q; expected route_type:ptv_stop_id", value)
	}
	routeType, routeTypeErr := strconv.Atoi(parts[0])
	stopID, stopIDErr := strconv.Atoi(parts[1])
	if routeTypeErr != nil || routeType < 0 || routeType > 4 || stopIDErr != nil || stopID <= 0 {
		return stationSeed{}, fmt.Errorf("invalid station %q; expected route_type:positive_ptv_stop_id", value)
	}
	return stationSeed{RouteType: routeType, StopID: stopID}, nil
}

func stationFacility(response *ptvapi.StopResponse) v3static.StationFacility {
	details := response.Stop
	result := v3static.StationFacility{
		RouteType: details.RouteType, PTVStopID: details.StopID, StopName: clean(details.StopName),
		StationType: clean(details.StationType), StationDescription: clean(details.StationDescription),
		StopLandmark: cleanPtr(details.StopLandmark),
	}
	if details.StopLocation != nil && details.StopLocation.GPS != nil {
		result.StopLocation = &v3static.Location{Latitude: details.StopLocation.GPS.Latitude, Longitude: details.StopLocation.GPS.Longitude}
	}
	if details.StopAmenities != nil {
		result.StopAmenities = &v3static.Amenities{Toilet: details.StopAmenities.Toilet, TaxiRank: details.StopAmenities.TaxiRank, CarParking: details.StopAmenities.CarParking, CCTV: details.StopAmenities.CCTV}
	}
	if details.StopAccessibility != nil {
		accessibility := details.StopAccessibility
		result.StopAccessibility = &v3static.Accessibility{
			Lighting: accessibility.Lighting, PlatformNumber: accessibility.PlatformNumber, AudioCustomerInformation: accessibility.AudioCustomerInformation,
			Escalator: accessibility.Escalator, HearingLoop: accessibility.HearingLoop, Lift: accessibility.Lift, Stairs: accessibility.Stairs,
			StopAccessible: accessibility.StopAccessible, TactileGroundSurfaceIndicator: accessibility.TactileGroundSurfaceIndicator, WaitingRoom: accessibility.WaitingRoom,
		}
		if accessibility.Wheelchair != nil {
			wheelchair := accessibility.Wheelchair
			result.StopAccessibility.Wheelchair = &v3static.Wheelchair{
				AccessibleRamp: wheelchair.AccessibleRamp, Parking: wheelchair.Parking, Telephone: wheelchair.Telephone, Toilet: wheelchair.Toilet,
				LowTicketCounter: wheelchair.LowTicketCounter, Manoeuvring: wheelchair.Manouvering, RaisedPlatform: wheelchair.RaisedPlatform,
				Ramp: wheelchair.Ramp, SecondaryPath: wheelchair.SecondaryPath, RaisedPlatformShelter: wheelchair.RaisedPlatformShelter, SteepRamp: wheelchair.SteepRamp,
			}
		}
	}
	if details.StopStaffing != nil {
		staffing := details.StopStaffing
		result.StopStaffing = &v3static.Staffing{
			MondayAMFrom: staffing.MonAMFrom, MondayAMTo: staffing.MonAMTo, MondayPMFrom: staffing.MonPMFrom, MondayPMTo: staffing.MonPMTo,
			TuesdayAMFrom: staffing.TueAMFrom, TuesdayAMTo: staffing.TueAMTo, TuesdayPMFrom: staffing.TuePMFrom, TuesdayPMTo: staffing.TuePMTo,
			WednesdayAMFrom: staffing.WedAMFrom, WednesdayAMTo: staffing.WedAMTo, WednesdayPMFrom: staffing.WedPMFrom, WednesdayPMTo: staffing.WedPMTo,
			ThursdayAMFrom: staffing.ThuAMFrom, ThursdayAMTo: staffing.ThuAMTo, ThursdayPMFrom: staffing.ThuPMFrom, ThursdayPMTo: staffing.ThuPMTo,
			FridayAMFrom: staffing.FriAMFrom, FridayAMTo: staffing.FriAMTo, FridayPMFrom: staffing.FriPMFrom, FridayPMTo: staffing.FriPMTo,
			SaturdayAMFrom: staffing.SatAMFrom, SaturdayAMTo: staffing.SatAMTo, SaturdayPMFrom: staffing.SatPMFrom, SaturdayPMTo: staffing.SatPMTo,
			SundayAMFrom: staffing.SunAMFrom, SundayAMTo: staffing.SunAMTo, SundayPMFrom: staffing.SunPMFrom, SundayPMTo: staffing.SunPMTo,
			PublicHolidayFrom: staffing.PHFrom, PublicHolidayTo: staffing.PHTo, PublicHolidayAdditionalText: staffing.PHAdditionalText,
		}
	}
	return result
}

func clean(value string) string { return strings.Join(strings.Fields(strings.TrimSpace(value)), " ") }

func cleanPtr(value *string) string {
	if value == nil {
		return ""
	}
	return clean(*value)
}
