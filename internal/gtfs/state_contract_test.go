package gtfs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFeedProvenanceJSONOmitsUnknownPublicationTime(t *testing.T) {
	raw, err := json.Marshal(FeedProvenance{
		SourceURL:   "https://example.test/gtfs.zip",
		ActualBytes: 123,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "publication_time") || strings.Contains(string(raw), "0001-01-01") {
		t.Fatalf("unknown publication evidence was serialized: %s", raw)
	}
}
