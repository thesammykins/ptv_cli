package gtfs

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/localtime"
)

func BenchmarkLoadTimetableContextRepresentative(b *testing.B) {
	const (
		tripCount = 200
		stopCount = 20
	)
	var stops, trips, stopTimes strings.Builder
	stops.WriteString("stop_id,stop_name,stop_lat,stop_lon\n")
	for stop := 0; stop < stopCount; stop++ {
		fmt.Fprintf(&stops, "s%d,Stop %d,-37.%04d,144.%04d\n", stop, stop, 8000+stop, 9000+stop)
	}
	trips.WriteString("route_id,service_id,trip_id,trip_headsign,direction_id\n")
	stopTimes.WriteString("trip_id,arrival_time,departure_time,stop_id,stop_sequence\n")
	for trip := 0; trip < tripCount; trip++ {
		fmt.Fprintf(&trips, "r1,svc,t%d,Destination,0\n", trip)
		for stop := 0; stop < stopCount; stop++ {
			seconds := 6*3600 + trip*30 + stop*120
			fmt.Fprintf(&stopTimes, "t%d,%02d:%02d:%02d,%02d:%02d:%02d,s%d,%d\n",
				trip, seconds/3600, (seconds/60)%60, seconds%60,
				seconds/3600, (seconds/60)%60, seconds%60, stop, stop+1)
		}
	}
	files := minimalFeed(map[string]string{
		"stops.txt":      stops.String(),
		"trips.txt":      trips.String(),
		"stop_times.txt": stopTimes.String(),
		"calendar.txt":   "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nsvc,1,1,1,1,1,1,1,20260701,20260731\n",
	})
	store, err := Open(filepath.Join(b.TempDir(), "benchmark.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	archive := filepath.Join(b.TempDir(), "feed.zip")
	writeOuterGTFSZip(b, archive, files)
	if _, err := IngestGeneration(b.Context(), store, archive, IngestGenerationOptions{
		GenerationID: "g-benchmark", Provenance: FeedProvenance{SourceURL: "https://example.test/gtfs.zip", ActualBytes: 1},
	}); err != nil {
		b.Fatal(err)
	}
	if _, err := store.db.ExecContext(b.Context(), indexes); err != nil {
		b.Fatal(err)
	}
	query := time.Date(2026, 7, 16, 7, 0, 0, 0, localtime.Melbourne())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		timetable, err := store.LoadTimetableContext(b.Context(), query, TimetableForward)
		if err != nil {
			b.Fatal(err)
		}
		if len(timetable.Connections) == 0 {
			b.Fatal("empty timetable")
		}
	}
}
