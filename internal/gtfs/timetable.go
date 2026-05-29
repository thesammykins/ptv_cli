package gtfs

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thesammykins/ptv_cli/internal/model"
)

// defaultTransferSeconds is used for GTFS transfers lacking a min time.
const defaultTransferSeconds = 120

// LoadTimetable builds the in-memory planning dataset for the given local day.
// It includes services active on that day, plus after-midnight running from
// the previous day's services (GTFS times >= 24:00:00).
func (s *Store) LoadTimetable(day time.Time) (*model.Timetable, error) {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	prev := day.AddDate(0, 0, -1)

	tt := &model.Timetable{
		Day:          day,
		TripRoute:    map[string]int{},
		TripHeadsign: map[string]string{},
		NameIndex:    map[string][]int{},
	}

	stopIdx, err := s.loadStops(tt)
	if err != nil {
		return nil, err
	}
	routeIdx, err := s.loadRoutes(tt)
	if err != nil {
		return nil, err
	}
	if err := s.loadTrips(tt, routeIdx); err != nil {
		return nil, err
	}

	dayActive, err := s.activeServices(day)
	if err != nil {
		return nil, err
	}
	prevActive, err := s.activeServices(prev)
	if err != nil {
		return nil, err
	}

	dayUnix := day.Unix()
	// Current-day connections (all segments).
	if err := s.appendConnections(tt, stopIdx, dayActive, dayUnix, 0, false); err != nil {
		return nil, err
	}
	// Previous-day overflow (segments that reach the query day).
	if err := s.appendConnections(tt, stopIdx, prevActive, prev.Unix(), 0, true); err != nil {
		return nil, err
	}

	sort.Slice(tt.Connections, func(i, j int) bool {
		return tt.Connections[i].DepTime < tt.Connections[j].DepTime
	})

	if err := s.loadFootpaths(tt, stopIdx); err != nil {
		return nil, err
	}
	tt.BuildStopModes()
	return tt, nil
}

func (s *Store) loadStops(tt *model.Timetable) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT stop_id, stop_name, stop_lat, stop_lon FROM stops`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[string]int{}
	for rows.Next() {
		var st model.Stop
		if err := rows.Scan(&st.ID, &st.Name, &st.Lat, &st.Lon); err != nil {
			return nil, err
		}
		st.Mode = feedModeFromID(st.ID)
		st.Index = len(tt.Stops)
		idx[st.ID] = st.Index
		tt.Stops = append(tt.Stops, st)
		key := strings.ToLower(st.Name)
		tt.NameIndex[key] = append(tt.NameIndex[key], st.Index)
	}
	return idx, rows.Err()
}

func (s *Store) loadRoutes(tt *model.Timetable) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT route_id, route_short_name, route_long_name, route_type, feed_mode FROM routes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[string]int{}
	for rows.Next() {
		var id string
		var routeType, feedMode int
		var ri model.RouteInfo
		if err := rows.Scan(&id, &ri.ShortName, &ri.LongName, &routeType, &feedMode); err != nil {
			return nil, err
		}
		ri.RouteType = modeFromRouteType(routeType, feedMode)
		idx[id] = len(tt.Routes)
		tt.Routes = append(tt.Routes, ri)
	}
	return idx, rows.Err()
}

func modeFromRouteType(routeType, feedMode int) int {
	if feedMode > 0 {
		return feedMode
	}
	switch routeType {
	case 0, 900, 901, 902, 903, 904, 905, 906:
		return 3
	case 1, 400, 401, 402, 403, 404, 405:
		return 2
	case 2, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109:
		return 1
	case 3, 700, 701, 702, 703, 704, 705, 706, 707, 708, 709:
		return 4
	case 200, 201, 202, 203, 204:
		return 5
	default:
		return feedMode
	}
}

// feedModeFromID extracts the PTV GTFS feed mode (the numeric prefix before the
// first ':') from a namespaced stop or route id. Returns -1 when absent.
func feedModeFromID(id string) int {
	i := strings.IndexByte(id, ':')
	if i <= 0 {
		return -1
	}
	n := 0
	for _, r := range id[:i] {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (s *Store) loadTrips(tt *model.Timetable, routeIdx map[string]int) error {
	rows, err := s.db.Query(`SELECT trip_id, route_id, trip_headsign FROM trips`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tripID, routeID, headsign string
		if err := rows.Scan(&tripID, &routeID, &headsign); err != nil {
			return err
		}
		if ri, ok := routeIdx[routeID]; ok {
			tt.TripRoute[tripID] = ri
		} else {
			tt.TripRoute[tripID] = -1
		}
		tt.TripHeadsign[tripID] = headsign
	}
	return rows.Err()
}

// activeServices returns the set of service_ids running on the given day.
func (s *Store) activeServices(day time.Time) (map[string]bool, error) {
	dateStr := day.Format("20060102")
	col := weekdayColumn(day.Weekday())

	active := map[string]bool{}
	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT service_id FROM calendar WHERE %s = 1 AND start_date <= ? AND end_date >= ?`, col),
		dateStr, dateStr)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		active[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	exRows, err := s.db.Query(`SELECT service_id, exception_type FROM calendar_dates WHERE date = ?`, dateStr)
	if err != nil {
		return nil, err
	}
	defer exRows.Close()
	for exRows.Next() {
		var id string
		var ex int
		if err := exRows.Scan(&id, &ex); err != nil {
			return nil, err
		}
		switch ex {
		case 1:
			active[id] = true
		case 2:
			delete(active, id)
		}
	}
	return active, exRows.Err()
}

// appendConnections builds elementary connections for active services. When
// overflowOnly is true, only segments arriving at/after 24:00:00 are emitted
// so after-midnight arrivals from previous-day services remain plannable.
func (s *Store) appendConnections(tt *model.Timetable, stopIdx map[string]int, active map[string]bool, baseUnix int64, _ int, overflowOnly bool) error {
	if len(active) == 0 {
		return nil
	}
	// Restrict the (very large) stop_times scan to active services via a
	// temporary table, which is dramatically faster than scanning every row.
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS active_svc`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TEMP TABLE active_svc (service_id TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	defer s.db.Exec(`DROP TABLE IF EXISTS active_svc`)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	ins, err := tx.Prepare(`INSERT OR IGNORE INTO active_svc (service_id) VALUES (?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for id := range active {
		if _, err := ins.Exec(id); err != nil {
			ins.Close()
			tx.Rollback()
			return err
		}
	}
	ins.Close()
	if err := tx.Commit(); err != nil {
		return err
	}

	rows, err := s.db.Query(`SELECT st.trip_id, st.stop_id, st.stop_sequence, st.arrival_sec, st.departure_sec
		FROM stop_times st
		JOIN trips t ON st.trip_id = t.trip_id
		JOIN active_svc a ON t.service_id = a.service_id
		ORDER BY st.trip_id, st.stop_sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		curTrip  string
		havePrev bool
		prevStop int
		prevDep  int
	)
	for rows.Next() {
		var tripID, stopID string
		var seq, arrSec, depSec int
		if err := rows.Scan(&tripID, &stopID, &seq, &arrSec, &depSec); err != nil {
			return err
		}
		if tripID != curTrip {
			curTrip = tripID
			havePrev = false
		}
		si, ok := stopIdx[stopID]
		if !ok {
			havePrev = false
			continue
		}
		if havePrev {
			emit := true
			if overflowOnly && arrSec < 86400 {
				emit = false
			}
			if emit {
				tt.Connections = append(tt.Connections, model.Connection{
					DepStop:  prevStop,
					ArrStop:  si,
					DepTime:  baseUnix + int64(prevDep),
					ArrTime:  baseUnix + int64(arrSec),
					TripID:   tripID,
					RouteIdx: tt.TripRoute[tripID],
				})
			}
		}
		prevStop = si
		prevDep = depSec
		havePrev = true
	}
	return rows.Err()
}

func (s *Store) loadFootpaths(tt *model.Timetable, stopIdx map[string]int) error {
	tt.Footpaths = make([][]model.Footpath, len(tt.Stops))
	rows, err := s.db.Query(`SELECT from_stop_id, to_stop_id, min_transfer_time FROM transfers`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var from, to string
		var mt int
		if err := rows.Scan(&from, &to, &mt); err != nil {
			return err
		}
		fi, ok1 := stopIdx[from]
		ti, ok2 := stopIdx[to]
		if !ok1 || !ok2 || fi == ti {
			continue
		}
		secs := mt
		if secs <= 0 {
			secs = defaultTransferSeconds
		}
		tt.Footpaths[fi] = append(tt.Footpaths[fi], model.Footpath{ToStop: ti, Seconds: secs})
	}
	return rows.Err()
}

// weekdayColumn maps a weekday to its calendar column name.
func weekdayColumn(d time.Weekday) string {
	switch d {
	case time.Monday:
		return "monday"
	case time.Tuesday:
		return "tuesday"
	case time.Wednesday:
		return "wednesday"
	case time.Thursday:
		return "thursday"
	case time.Friday:
		return "friday"
	case time.Saturday:
		return "saturday"
	default:
		return "sunday"
	}
}
