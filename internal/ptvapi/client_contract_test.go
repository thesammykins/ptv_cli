package ptvapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStopDetailsMatchesOfficialNestedContract(t *testing.T) {
	fixture := readFixture(t, "testdata/stop_details.json")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v3/stops/1071/route_type/0" {
			t.Errorf("path = %q", r.URL.Path)
		}
		for _, name := range []string{"stop_location", "stop_amenities", "stop_accessibility", "stop_staffing", "stop_disruptions"} {
			if r.URL.Query().Get(name) != "true" {
				t.Errorf("%s = %q, want true", name, r.URL.Query().Get(name))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	client := NewWithOptions(server.URL, "test-key", "123", ClientOptions{HTTPClient: server.Client()})
	response, err := client.StopDetails(context.Background(), 1071, 0)
	if err != nil {
		t.Fatalf("StopDetails: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	stop := response.Stop
	if stop.StopLocation == nil || stop.StopLocation.GPS == nil || stop.StopLocation.GPS.Latitude == nil || stop.StopLocation.GPS.Longitude == nil {
		t.Fatalf("nested GPS not modeled: %+v", stop.StopLocation)
	}
	if *stop.StopLocation.GPS.Latitude != -37.818175 || *stop.StopLocation.GPS.Longitude != 144.966776 {
		t.Fatalf("nested GPS = %f,%f", *stop.StopLocation.GPS.Latitude, *stop.StopLocation.GPS.Longitude)
	}
	if stop.StopAmenities == nil || stop.StopAmenities.Toilet == nil || !*stop.StopAmenities.Toilet || stop.StopAmenities.CarParking == nil || *stop.StopAmenities.CarParking != "12" {
		t.Fatalf("amenities not modeled: %+v", stop.StopAmenities)
	}
	if stop.StopAccessibility == nil || stop.StopAccessibility.Wheelchair == nil || stop.StopAccessibility.Lift == nil || !*stop.StopAccessibility.Lift {
		t.Fatalf("accessibility not modeled: %+v", stop.StopAccessibility)
	}
	if stop.StopStaffing == nil || stop.StopStaffing.WedPMTo == nil || *stop.StopStaffing.WedPMTo != "23:59" {
		t.Fatalf("staffing not modeled: %+v", stop.StopStaffing)
	}
	if len(stop.DisruptionIDs) != 1 || response.Disruptions["101"].DisruptionID != 101 {
		t.Fatalf("disruptions not modeled: stop=%v response=%v", stop.DisruptionIDs, response.Disruptions)
	}
	if len(stop.Routes) != 1 || stop.StopLandmark == nil || *stop.StopLandmark != "Flinders Street" {
		t.Fatalf("route/landmark not modeled: %+v", stop)
	}
}

func TestRunsByRefUsesOneBroadRequest(t *testing.T) {
	fixture := readFixture(t, "testdata/runs_by_ref.json")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v3/runs/run-123" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query()["expand"]; len(got) != 2 || got[0] != "1" || got[1] != "2" {
			t.Errorf("expand = %v", got)
		}
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	client := NewWithOptions(server.URL, "test-key", "123", ClientOptions{HTTPClient: server.Client()})
	response, err := client.RunsByRef(context.Background(), RunRef("run-123"), RunsOptions{Expand: []string{ExpandVehiclePosition, ExpandVehicleDescriptor}})
	if err != nil {
		t.Fatalf("RunsByRef: %v", err)
	}
	if requests.Load() != 1 || len(response.Runs) != 1 || response.Runs[0].RunRef != "run-123" {
		t.Fatalf("requests=%d response=%+v", requests.Load(), response)
	}
}

func TestRoutesUsesOneMultiValuedRequest(t *testing.T) {
	fixture := readFixture(t, "testdata/routes.json")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		got := r.URL.Query()["route_types"]
		if strings.Join(got, ",") != "0,1,2,3" {
			t.Errorf("route_types = %v", got)
		}
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	client := NewWithOptions(server.URL, "test-key", "123", ClientOptions{HTTPClient: server.Client()})
	response, err := client.Routes(context.Background(), []int{0, 1, 2, 3}, "")
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if requests.Load() != 1 || len(response.Routes) != 2 {
		t.Fatalf("requests=%d routes=%d", requests.Load(), len(response.Routes))
	}
}

func TestPTVResponseBodyIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 65))
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, "test-key", "123", ClientOptions{
		HTTPClient:       server.Client(),
		MaxResponseBytes: 64,
	})

	_, err := client.RouteTypes(context.Background())
	if !IsKind(err, ErrorInvalidResponse) || !strings.Contains(err.Error(), "64-byte limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestPTVStatusErrorsAreTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9999")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"slow down","status":{"version":"3.0","health":0}}`)
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, "test-key", "123", ClientOptions{HTTPClient: server.Client()})

	_, err := client.RouteTypes(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != ErrorRateLimit || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %#v", err)
	}
	if apiErr.RetryAfter != maxRetryAfter {
		t.Fatalf("RetryAfter = %v, want %v", apiErr.RetryAfter, maxRetryAfter)
	}
}

func TestPTVTransportErrorDoesNotExposeSignedURL(t *testing.T) {
	client := NewWithOptions("https://example.invalid", "top-secret", "123", ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &url.Error{
				Op:  "Get",
				URL: "https://example.invalid/v3/routes?devid=123&signature=SECRET",
				Err: errors.New("dial failed for signature=SECRET and devid=123"),
			}
		})},
	})

	_, err := client.RouteTypes(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "secret") || strings.Contains(lower, "devid=123") || strings.Contains(lower, "signature=secret") {
		t.Fatalf("error leaked signed request data: %v", err)
	}
}

func TestPTVCancellationIsTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewWithOptions("https://example.invalid", "test", "123", ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, r.Context().Err()
		})},
	})
	_, err := client.RouteTypes(ctx)
	if !IsKind(err, ErrorCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestPTVCancellationDuringBodyReadIsTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	client := NewWithOptions("https://example.invalid", "test", "123", ClientOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       &contextResponseBody{ctx: request.Context(), started: started},
			}, nil
		})},
	})
	result := make(chan error, 1)
	go func() {
		_, err := client.RouteTypes(ctx)
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !IsKind(err, ErrorCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RouteTypes did not stop after body-read cancellation")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type contextResponseBody struct {
	ctx     context.Context
	started chan struct{}
}

func (body *contextResponseBody) Read([]byte) (int, error) {
	close(body.started)
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (*contextResponseBody) Close() error { return nil }

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(10*time.Second).Format(http.TimeFormat), now); got != 10*time.Second {
		t.Fatalf("parseRetryAfter = %v", got)
	}
}
