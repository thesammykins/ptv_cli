package cmd

import (
	"strings"

	"github.com/thesammykins/ptv_cli/internal/ptvapi"
)

func chooseStop(query string, stops []ptvapi.StopModel) *ptvapi.StopModel {
	query = strings.TrimSpace(query)
	for i := range stops {
		if strings.EqualFold(stops[i].StopName, query) {
			return &stops[i]
		}
	}
	stationName := query
	if !strings.Contains(strings.ToLower(stationName), "station") {
		stationName += " Station"
	}
	for i := range stops {
		if strings.EqualFold(stops[i].StopName, stationName) {
			return &stops[i]
		}
	}
	return &stops[0]
}
