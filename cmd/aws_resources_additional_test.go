package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func testS3DeleteClient(t *testing.T, handler http.HandlerFunc) *s3.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return s3.New(s3.Options{
		Region:       testRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
		BaseEndpoint: sdkaws.String(server.URL),
		UsePathStyle: true,
		HTTPClient:   server.Client(),
	})
}

func testEventBridgeClient(t *testing.T, handler http.HandlerFunc) *eventbridge.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return eventbridge.New(eventbridge.Options{
		Region:       testRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
		BaseEndpoint: sdkaws.String(server.URL),
		HTTPClient:   server.Client(),
	})
}

func TestDeleteS3ObjectVersionsDeletesEachVersion(t *testing.T) {
	t.Parallel()

	deleteCalls := 0
	client := testS3DeleteClient(t, func(w http.ResponseWriter, r *http.Request) {
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	err := deleteS3ObjectVersions(context.Background(), client, "bucket", []s3types.ObjectVersion{{Key: sdkaws.String("a"), VersionId: sdkaws.String("1")}, {Key: sdkaws.String("b"), VersionId: sdkaws.String("2")}})
	if err != nil {
		t.Fatalf("deleteS3ObjectVersions: %v", err)
	}
	if deleteCalls != 2 {
		t.Fatalf("delete calls = %d, want 2", deleteCalls)
	}
}

func TestDeleteS3DeleteMarkersDeletesEachMarker(t *testing.T) {
	t.Parallel()

	deleteCalls := 0
	client := testS3DeleteClient(t, func(w http.ResponseWriter, r *http.Request) {
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	err := deleteS3DeleteMarkers(context.Background(), client, "bucket", []s3types.DeleteMarkerEntry{{Key: sdkaws.String("a"), VersionId: sdkaws.String("1")}})
	if err != nil {
		t.Fatalf("deleteS3DeleteMarkers: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
}

func TestDeleteS3ObjectVersionsReturnsDeleteError(t *testing.T) {
	t.Parallel()

	client := testS3DeleteClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	err := deleteS3ObjectVersions(context.Background(), client, "bucket", []s3types.ObjectVersion{{Key: sdkaws.String("a"), VersionId: sdkaws.String("1")}})
	if err == nil {
		t.Fatal("expected deleteS3ObjectVersions to return an error")
	}
}

func TestDeleteS3DeleteMarkersReturnsDeleteError(t *testing.T) {
	t.Parallel()

	client := testS3DeleteClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	err := deleteS3DeleteMarkers(context.Background(), client, "bucket", []s3types.DeleteMarkerEntry{{Key: sdkaws.String("a"), VersionId: sdkaws.String("1")}})
	if err == nil {
		t.Fatal("expected deleteS3DeleteMarkers to return an error")
	}
}

func TestListEventBridgeTargetIDsPaginates(t *testing.T) {
	t.Parallel()

	call := 0
	client := testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if call == 1 {
			_, _ = io.WriteString(w, `{"Targets":[{"Id":"one"}],"NextToken":"next"}`)
			return
		}
		_, _ = io.WriteString(w, `{"Targets":[{"Id":"two"}]}`)
	})
	targets, err := listEventBridgeTargetIDs(context.Background(), client, "rule")
	if err != nil {
		t.Fatalf("listEventBridgeTargetIDs: %v", err)
	}
	if len(targets) != 2 || targets[0] != "one" || targets[1] != "two" {
		t.Fatalf("unexpected targets: %v", targets)
	}
}

func TestDeleteEventBridgeRuleTargetsBatchesRequests(t *testing.T) {
	t.Parallel()

	removeBatchSizes := []int{}
	client := testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		target := r.Header.Get("X-Amz-Target")
		switch {
		case strings.Contains(target, "ListTargetsByRule"):
			_, _ = io.WriteString(w, `{"Targets":[{"Id":"1"},{"Id":"2"},{"Id":"3"},{"Id":"4"},{"Id":"5"},{"Id":"6"},{"Id":"7"},{"Id":"8"},{"Id":"9"},{"Id":"10"},{"Id":"11"}]}`)
		case strings.Contains(target, "RemoveTargets"):
			var payload struct {
				IDs []string `json:"Ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode RemoveTargets payload: %v", err)
			}
			removeBatchSizes = append(removeBatchSizes, len(payload.IDs))
			_, _ = io.WriteString(w, `{"FailedEntryCount":0}`)
		default:
			t.Fatalf("unexpected target header: %q", target)
		}
	})

	if err := deleteEventBridgeRuleTargets(context.Background(), client, "rule"); err != nil {
		t.Fatalf("deleteEventBridgeRuleTargets: %v", err)
	}
	if len(removeBatchSizes) != 2 || removeBatchSizes[0] != 10 || removeBatchSizes[1] != 1 {
		t.Fatalf("unexpected remove batch sizes: %v", removeBatchSizes)
	}
}

func TestDeleteEventBridgeRuleTargetsReturnsListError(t *testing.T) {
	t.Parallel()

	client := testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"__type":"InternalException","message":"boom"}`)
	})
	err := deleteEventBridgeRuleTargets(context.Background(), client, "rule")
	if err == nil {
		t.Fatal("expected deleteEventBridgeRuleTargets to return an error")
	}
}

func TestRemoveEventBridgeTargetBatchReturnsFailedIDs(t *testing.T) {
	t.Parallel()

	client := testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = io.WriteString(w, `{"FailedEntryCount":1,"FailedEntries":[{"TargetId":"bad-target"}]}`)
	})
	err := removeEventBridgeTargetBatch(context.Background(), client, "rule", []string{"bad-target"})
	if err == nil || !strings.Contains(err.Error(), "bad-target") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemoveEventBridgeTargetBatchSucceedsWithoutFailures(t *testing.T) {
	t.Parallel()

	client := testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = io.WriteString(w, `{"FailedEntryCount":0}`)
	})
	if err := removeEventBridgeTargetBatch(context.Background(), client, "rule", []string{"ok"}); err != nil {
		t.Fatalf("removeEventBridgeTargetBatch: %v", err)
	}
}

func TestRemoveEventBridgeTargetBatchReturnsCountWhenEntriesLackIDs(t *testing.T) {
	t.Parallel()

	client := testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = io.WriteString(w, `{"FailedEntryCount":1,"FailedEntries":[{}]}`)
	})
	err := removeEventBridgeTargetBatch(context.Background(), client, "rule", []string{"ok"})
	if err == nil || !strings.Contains(err.Error(), "failed to remove 1 EventBridge target") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsTargetsStillPresentError(t *testing.T) {
	t.Parallel()

	if !isTargetsStillPresentError(errors.New("Rule can't be deleted since it has targets attached")) {
		t.Fatal("expected targets-still-present error")
	}
	if isTargetsStillPresentError(errors.New("validation failed")) {
		t.Fatal("unexpected targets-still-present classification")
	}
}

func TestDeleteResourceNativelyHandlesUnknownAndForceGatedTypes(t *testing.T) {
	t.Parallel()

	handled, err := deleteResourceNatively(context.Background(), auditResource{resourceType: "unknown"}, false)
	if err != nil || handled {
		t.Fatalf("unknown resource: handled=%v err=%v", handled, err)
	}
	handled, err = deleteResourceNatively(context.Background(), auditResource{resourceType: "ec2/internet-gateway", name: "igw-123"}, false)
	if err != nil || handled {
		t.Fatalf("force-gated resource: handled=%v err=%v", handled, err)
	}
}
