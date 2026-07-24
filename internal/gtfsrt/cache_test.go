package gtfsrt

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

func TestInvocationCacheDeduplicatesConcurrentFeedFetches(t *testing.T) {
	var requests atomic.Int32
	client := NewWithOptions("key", ClientOptions{HTTPClient: &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		time.Sleep(20 * time.Millisecond)
		return protobufResponse(http.StatusOK, mustMarshal(vehicleFeed(time.Now(), time.Now(), "label", "private"))), nil
	})}})
	cache := NewInvocationCache()
	feed := testFeed()
	var wg sync.WaitGroup
	results := make([]*Snapshot, 8)
	errorsSeen := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errorsSeen[i] = cache.GetOrFetch(context.Background(), client, feed)
		}(i)
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
	for i := range results {
		if errorsSeen[i] != nil || results[i] == nil {
			t.Fatalf("result[%d]=%v %v", i, results[i], errorsSeen[i])
		}
	}
}

func mustMarshal(message proto.Message) []byte {
	body, err := proto.Marshal(message)
	if err != nil {
		panic(err)
	}
	return body
}
