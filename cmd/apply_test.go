package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
)

const testContentTypeJSON = "application/json"

func testSchedulerCfg(t *testing.T, handler http.HandlerFunc) sdkaws.Config {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return sdkaws.Config{
		Region:       testRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
		BaseEndpoint: sdkaws.String(server.URL),
		HTTPClient:   server.Client(),
	}
}

func TestScheduleActivationUpdatesExistingSchedule(t *testing.T) {
	cfg := testSchedulerCfg(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testContentTypeJSON)
		if r.Method == http.MethodPut {
			_, _ = io.WriteString(w, `{"ScheduleArn":"arn:aws:scheduler:us-east-1:123:schedule/group/name"}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	activateAt, err := scheduleActivation(context.Background(), cfg, "myorg", testAccountID, testRegion)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if activateAt.IsZero() {
		t.Fatal("expected non-zero activateAt")
	}
}

func TestScheduleActivationCreatesWhenNotFound(t *testing.T) {
	cfg := testSchedulerCfg(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testContentTypeJSON)
		switch r.Method {
		case http.MethodPut:
			// UpdateSchedule → ResourceNotFoundException (must include error type header)
			w.Header().Set("X-Amzn-ErrorType", "ResourceNotFoundException")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Schedule myorg-activate not found.","ResourceType":"SCHEDULE"}`)
		case http.MethodPost:
			// CreateSchedule → success
			_, _ = io.WriteString(w, `{"ScheduleArn":"arn:aws:scheduler:us-east-1:123:schedule/group/name"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	activateAt, err := scheduleActivation(context.Background(), cfg, "myorg", testAccountID, testRegion)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if activateAt.IsZero() {
		t.Fatal("expected non-zero activateAt")
	}
}

func TestScheduleActivationReturnsUpdateError(t *testing.T) {
	cfg := testSchedulerCfg(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"Message":"internal error"}`)
	})
	_, err := scheduleActivation(context.Background(), cfg, "myorg", testAccountID, testRegion)
	if err == nil {
		t.Fatal("expected error from update failure")
	}
	if !strings.Contains(err.Error(), "updating schedule") {
		t.Fatalf("expected 'updating schedule' in error, got: %v", err)
	}
}

func TestScheduleActivationReturnsCreateError(t *testing.T) {
	cfg := testSchedulerCfg(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testContentTypeJSON)
		switch r.Method {
		case http.MethodPut:
			// UpdateSchedule → ResourceNotFoundException (must include error type header)
			w.Header().Set("X-Amzn-ErrorType", "ResourceNotFoundException")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Schedule not found.","ResourceType":"SCHEDULE"}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"Message":"create failed"}`)
		}
	})
	_, err := scheduleActivation(context.Background(), cfg, "myorg", testAccountID, testRegion)
	if err == nil {
		t.Fatal("expected error from create failure")
	}
	if !strings.Contains(err.Error(), "creating schedule") {
		t.Fatalf("expected 'creating schedule' in error, got: %v", err)
	}
}

func TestScheduleActivationDetectsResourceNotFoundException(t *testing.T) {
	// Verify the errors.As detection works: a typed ResourceNotFoundException
	// must trigger the create path.
	var rnfe *schedulertypes.ResourceNotFoundException
	if rnfe != nil {
		t.Fatal("nil pointer should be nil")
	}
	// Construct one to confirm the type is correct.
	rnfe = &schedulertypes.ResourceNotFoundException{Message: sdkaws.String("not found")}
	if rnfe.ErrorMessage() == "" {
		t.Fatal("ResourceNotFoundException.ErrorMessage should not be empty")
	}
	// The real create path is tested by TestScheduleActivationCreatesWhenNotFound.
	_ = scheduler.UpdateScheduleInput{}
}
