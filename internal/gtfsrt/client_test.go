package gtfsrt

import "testing"

func TestVehicleIdentifierMatchesConsistComponent(t *testing.T) {
	if !vehicleIdentifierMatches("1041T-1098T-381M-382M-495M-496M", "381M") {
		t.Fatal("expected consist component to match")
	}
}

func TestVehicleIdentifierMatchesExactOnlyForPlainIDs(t *testing.T) {
	if vehicleIdentifierMatches("BS04FR", "BS04") {
		t.Fatal("unexpected partial plain id match")
	}
}
