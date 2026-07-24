// Package v3static contains the reviewed, generated subset of PTV Timetable
// API v3 data that can be used without v3 credentials. It is supplementary
// metadata only; GTFS and GTFS-Realtime remain the authoritative sources for
// static topology, schedules, and live state.
package v3static

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	SchemaVersion  = 2
	Source         = "ptv_timetable_api_v3"
	Attribution    = "Source: Licensed from Public Transport Victoria under a Creative Commons Attribution 4.0 International Licence."
	LicenseURL     = "https://creativecommons.org/licenses/by/4.0/"
	SourceURL      = "https://www.ptv.vic.gov.au/footer/data-and-reporting/datasets/ptv-timetable-api/"
	Modification   = "This bundled snapshot is a normalized, selected subset of PTV Timetable API data and may be out of date."
	Disclaimer     = "The PTV Timetable API Data is provided as is. Use of this data is your responsibility; determine whether it is suitable for your purposes."
	NonEndorsement = "ptv is an independent project and is not affiliated with or endorsed by Public Transport Victoria."
)

// Snapshot is the normalized, public-data boundary embedded in the CLI.
// GeneratedAt is metadata and ContentHash covers only the actual records, so
// unchanged refreshes do not produce noisy diffs.
type Snapshot struct {
	SchemaVersion     int               `json:"schema_version"`
	GeneratedAt       string            `json:"generated_at"`
	Source            string            `json:"source"`
	Attribution       string            `json:"attribution"`
	License           string            `json:"license"`
	SourceURL         string            `json:"source_url"`
	Modification      string            `json:"modification"`
	Disclaimer        string            `json:"disclaimer"`
	NonEndorsement    string            `json:"non_endorsement"`
	ContentHash       string            `json:"content_sha256"`
	Outlets           []Outlet          `json:"outlets,omitempty"`
	StationFacilities []StationFacility `json:"station_facilities,omitempty"`
}

type Outlet struct {
	OutletName      string  `json:"outlet_name"`
	OutletBusiness  string  `json:"outlet_business"`
	OutletLatitude  float64 `json:"outlet_latitude"`
	OutletLongitude float64 `json:"outlet_longitude"`
	OutletSuburb    string  `json:"outlet_suburb"`
}

// StationFacility deliberately mirrors only stable public facility fields.
// Disruptions and timestamps are excluded because they are live data.
type StationFacility struct {
	RouteType          int            `json:"route_type"`
	PTVStopID          int            `json:"ptv_stop_id"`
	StopName           string         `json:"stop_name"`
	StationType        string         `json:"station_type,omitempty"`
	StationDescription string         `json:"station_description,omitempty"`
	StopLandmark       string         `json:"stop_landmark,omitempty"`
	StopLocation       *Location      `json:"stop_location,omitempty"`
	StopAmenities      *Amenities     `json:"stop_amenities,omitempty"`
	StopAccessibility  *Accessibility `json:"stop_accessibility,omitempty"`
	StopStaffing       *Staffing      `json:"stop_staffing,omitempty"`
}

type Location struct {
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

type Amenities struct {
	Toilet     *bool   `json:"toilet,omitempty"`
	TaxiRank   *bool   `json:"taxi_rank,omitempty"`
	CarParking *string `json:"car_parking,omitempty"`
	CCTV       *bool   `json:"cctv,omitempty"`
}

type Accessibility struct {
	Lighting                      *bool       `json:"lighting,omitempty"`
	PlatformNumber                *int        `json:"platform_number,omitempty"`
	AudioCustomerInformation      *bool       `json:"audio_customer_information,omitempty"`
	Escalator                     *bool       `json:"escalator,omitempty"`
	HearingLoop                   *bool       `json:"hearing_loop,omitempty"`
	Lift                          *bool       `json:"lift,omitempty"`
	Stairs                        *bool       `json:"stairs,omitempty"`
	StopAccessible                *bool       `json:"stop_accessible,omitempty"`
	TactileGroundSurfaceIndicator *bool       `json:"tactile_ground_surface_indicator,omitempty"`
	WaitingRoom                   *bool       `json:"waiting_room,omitempty"`
	Wheelchair                    *Wheelchair `json:"wheelchair,omitempty"`
}

type Wheelchair struct {
	AccessibleRamp        *bool `json:"accessible_ramp,omitempty"`
	Parking               *bool `json:"parking,omitempty"`
	Telephone             *bool `json:"telephone,omitempty"`
	Toilet                *bool `json:"toilet,omitempty"`
	LowTicketCounter      *bool `json:"low_ticket_counter,omitempty"`
	Manoeuvring           *bool `json:"manoeuvring,omitempty"`
	RaisedPlatform        *bool `json:"raised_platform,omitempty"`
	Ramp                  *bool `json:"ramp,omitempty"`
	SecondaryPath         *bool `json:"secondary_path,omitempty"`
	RaisedPlatformShelter *bool `json:"raised_platform_shelter,omitempty"`
	SteepRamp             *bool `json:"steep_ramp,omitempty"`
}

type Staffing struct {
	MondayAMFrom                *string `json:"monday_am_from,omitempty"`
	MondayAMTo                  *string `json:"monday_am_to,omitempty"`
	MondayPMFrom                *string `json:"monday_pm_from,omitempty"`
	MondayPMTo                  *string `json:"monday_pm_to,omitempty"`
	TuesdayAMFrom               *string `json:"tuesday_am_from,omitempty"`
	TuesdayAMTo                 *string `json:"tuesday_am_to,omitempty"`
	TuesdayPMFrom               *string `json:"tuesday_pm_from,omitempty"`
	TuesdayPMTo                 *string `json:"tuesday_pm_to,omitempty"`
	WednesdayAMFrom             *string `json:"wednesday_am_from,omitempty"`
	WednesdayAMTo               *string `json:"wednesday_am_to,omitempty"`
	WednesdayPMFrom             *string `json:"wednesday_pm_from,omitempty"`
	WednesdayPMTo               *string `json:"wednesday_pm_to,omitempty"`
	ThursdayAMFrom              *string `json:"thursday_am_from,omitempty"`
	ThursdayAMTo                *string `json:"thursday_am_to,omitempty"`
	ThursdayPMFrom              *string `json:"thursday_pm_from,omitempty"`
	ThursdayPMTo                *string `json:"thursday_pm_to,omitempty"`
	FridayAMFrom                *string `json:"friday_am_from,omitempty"`
	FridayAMTo                  *string `json:"friday_am_to,omitempty"`
	FridayPMFrom                *string `json:"friday_pm_from,omitempty"`
	FridayPMTo                  *string `json:"friday_pm_to,omitempty"`
	SaturdayAMFrom              *string `json:"saturday_am_from,omitempty"`
	SaturdayAMTo                *string `json:"saturday_am_to,omitempty"`
	SaturdayPMFrom              *string `json:"saturday_pm_from,omitempty"`
	SaturdayPMTo                *string `json:"saturday_pm_to,omitempty"`
	SundayAMFrom                *string `json:"sunday_am_from,omitempty"`
	SundayAMTo                  *string `json:"sunday_am_to,omitempty"`
	SundayPMFrom                *string `json:"sunday_pm_from,omitempty"`
	SundayPMTo                  *string `json:"sunday_pm_to,omitempty"`
	PublicHolidayFrom           *string `json:"public_holiday_from,omitempty"`
	PublicHolidayTo             *string `json:"public_holiday_to,omitempty"`
	PublicHolidayAdditionalText *string `json:"public_holiday_additional_text,omitempty"`
}

type content struct {
	Outlets           []Outlet          `json:"outlets,omitempty"`
	StationFacilities []StationFacility `json:"station_facilities,omitempty"`
}

func (s *Snapshot) normalize() {
	if s == nil {
		return
	}
	s.SchemaVersion = SchemaVersion
	s.Source = Source
	s.Attribution = Attribution
	s.License = LicenseURL
	s.SourceURL = SourceURL
	s.Modification = Modification
	s.Disclaimer = Disclaimer
	s.NonEndorsement = NonEndorsement
	sort.Slice(s.Outlets, func(i, j int) bool {
		if s.Outlets[i].OutletSuburb != s.Outlets[j].OutletSuburb {
			return s.Outlets[i].OutletSuburb < s.Outlets[j].OutletSuburb
		}
		if s.Outlets[i].OutletName != s.Outlets[j].OutletName {
			return s.Outlets[i].OutletName < s.Outlets[j].OutletName
		}
		return s.Outlets[i].OutletBusiness < s.Outlets[j].OutletBusiness
	})
	sort.Slice(s.StationFacilities, func(i, j int) bool {
		if s.StationFacilities[i].RouteType != s.StationFacilities[j].RouteType {
			return s.StationFacilities[i].RouteType < s.StationFacilities[j].RouteType
		}
		return s.StationFacilities[i].PTVStopID < s.StationFacilities[j].PTVStopID
	})
}

func (s *Snapshot) contentBytes() ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("nil v3 static snapshot")
	}
	c := content{Outlets: s.Outlets, StationFacilities: s.StationFacilities}
	return json.Marshal(c)
}

// RecomputeHash canonicalizes the records and updates ContentHash.
func (s *Snapshot) RecomputeHash() error {
	s.normalize()
	data, err := s.contentBytes()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	s.ContentHash = hex.EncodeToString(digest[:])
	return nil
}

// Validate verifies the immutable metadata and content digest.
func (s *Snapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("nil v3 static snapshot")
	}
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported v3 static schema version %d", s.SchemaVersion)
	}
	if s.Source != Source || s.Attribution != Attribution || s.License != LicenseURL || s.SourceURL != SourceURL || s.Modification != Modification || s.Disclaimer != Disclaimer || s.NonEndorsement != NonEndorsement {
		return fmt.Errorf("invalid v3 static provenance metadata")
	}
	if strings.TrimSpace(s.GeneratedAt) == "" || len(s.ContentHash) != sha256.Size*2 {
		return fmt.Errorf("invalid v3 static snapshot metadata")
	}
	copySnapshot := *s
	if err := copySnapshot.RecomputeHash(); err != nil {
		return err
	}
	if copySnapshot.ContentHash != s.ContentHash {
		return fmt.Errorf("v3 static content hash mismatch")
	}
	return nil
}
