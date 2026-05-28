package gtfs

import "testing"

func TestFeedModeFromID(t *testing.T) {
	cases := []struct {
		id   string
		want int
	}{
		{"2:11217", 2},
		{"4:30817", 4},
		{"11:55", 11},
		{"noprefix", -1},
		{"abc:123", -1},
		{":123", -1},
	}
	for _, c := range cases {
		if got := feedModeFromID(c.id); got != c.want {
			t.Errorf("feedModeFromID(%q) = %d, want %d", c.id, got, c.want)
		}
	}
}
