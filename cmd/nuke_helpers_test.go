package cmd

import (
	"context"
	"errors"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
)

func TestDeletePendingScheduleIgnoresNotFound(t *testing.T) {
	oldDelete := deleteScheduleFn
	t.Cleanup(func() { deleteScheduleFn = oldDelete })
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return nil, errors.New("resource not found: missing")
	}

	if err := deletePendingSchedule(context.Background(), "group", "name"); err != nil {
		t.Fatalf("deletePendingSchedule: %v", err)
	}
}

func TestDeleteSchedulePageSkipsEmptyNamesAndCollectsRemoved(t *testing.T) {
	oldDelete := deleteScheduleFn
	t.Cleanup(func() { deleteScheduleFn = oldDelete })
	var removed []string
	deleteScheduleFn = func(_ context.Context, input *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		removed = append(removed, sdkaws.ToString(input.Name))
		return &scheduler.DeleteScheduleOutput{}, nil
	}

	out, err := deleteSchedulePage(context.Background(), "group", []schedulertypes.ScheduleSummary{
		{Name: sdkaws.String("")},
		{Name: sdkaws.String("a")},
		{Name: sdkaws.String("b")},
	})
	if err != nil {
		t.Fatalf("deleteSchedulePage: %v", err)
	}
	if len(out) != 2 || out[0] != "a" || out[1] != "b" {
		t.Fatalf("removed schedules = %v", out)
	}
	if len(removed) != 2 {
		t.Fatalf("delete calls = %v, want 2", removed)
	}
}

func TestDeleteSchedulePageReturnsDeleteError(t *testing.T) {
	oldDelete := deleteScheduleFn
	t.Cleanup(func() { deleteScheduleFn = oldDelete })
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return nil, errors.New("boom")
	}
	_, err := deleteSchedulePage(context.Background(), "group", []schedulertypes.ScheduleSummary{{Name: sdkaws.String("a")}})
	if err == nil || err.Error() != "boom" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestDeletePendingSchedulesReturnsCollectedNamesOnNotFoundPage(t *testing.T) {
	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	t.Cleanup(func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
	})
	listCalls := 0
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		listCalls++
		if listCalls == 1 {
			return &scheduler.ListSchedulesOutput{Schedules: []schedulertypes.ScheduleSummary{{Name: sdkaws.String("a")}}}, nil
		}
		return nil, errors.New("resource not found")
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	removed, err := deletePendingSchedules(context.Background(), "org")
	if err != nil {
		t.Fatalf("deletePendingSchedules: %v", err)
	}
	if len(removed) != 1 || removed[0] != "a" {
		t.Fatalf("unexpected removed schedules: %v", removed)
	}
}

func TestDeletePendingScheduleReturnsUnexpectedError(t *testing.T) {
	oldDelete := deleteScheduleFn
	t.Cleanup(func() { deleteScheduleFn = oldDelete })
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return nil, errors.New("boom")
	}
	err := deletePendingSchedule(context.Background(), "group", "name")
	if err == nil || err.Error() != "boom" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestDeletePendingSchedulesReturnsListError(t *testing.T) {
	oldList := listSchedulesFn
	t.Cleanup(func() { listSchedulesFn = oldList })
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return nil, errors.New("list failed")
	}
	_, err := deletePendingSchedules(context.Background(), "org")
	if err == nil || err.Error() != "list failed" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestDeletePendingSchedulesReturnsDeleteError(t *testing.T) {
	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	t.Cleanup(func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
	})
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{Schedules: []schedulertypes.ScheduleSummary{{Name: sdkaws.String("a")}}}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return nil, errors.New("delete failed")
	}
	_, err := deletePendingSchedules(context.Background(), "org")
	if err == nil || err.Error() != "delete failed" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestCleanupInventorySourcesForNukeWrapsSourceError(t *testing.T) {
	oldSources := inventorySourcesFn
	t.Cleanup(func() { inventorySourcesFn = oldSources })
	inventorySourcesFn = func() []inventorySource {
		return []inventorySource{stubInventorySource{id: "runtime", cleanupFn: func(context.Context) ([]string, error) {
			return nil, errors.New("boom")
		}}}
	}
	_, err := cleanupInventorySourcesForNuke(context.Background())
	if err == nil || err.Error() != "runtime cleanup: boom" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestCleanupInventorySourcesForNukeFormatsMessages(t *testing.T) {
	oldSources := inventorySourcesFn
	t.Cleanup(func() { inventorySourcesFn = oldSources })
	inventorySourcesFn = func() []inventorySource {
		return []inventorySource{stubInventorySource{id: "runtime", cleanupFn: func(context.Context) ([]string, error) {
			return []string{"schedule-a", "schedule-b"}, nil
		}}}
	}
	messages, err := cleanupInventorySourcesForNuke(context.Background())
	if err != nil {
		t.Fatalf("cleanupInventorySourcesForNuke: %v", err)
	}
	if len(messages) != 2 || messages[0] != "runtime: schedule-a" || messages[1] != "runtime: schedule-b" {
		t.Fatalf("unexpected messages: %v", messages)
	}
}

func TestDeletePendingSchedulesPaginates(t *testing.T) {
	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	t.Cleanup(func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
	})
	listCalls := 0
	listSchedulesFn = func(_ context.Context, input *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		listCalls++
		if listCalls == 1 {
			if sdkaws.ToString(input.NextToken) != "" {
				t.Fatalf("unexpected next token on first page: %q", sdkaws.ToString(input.NextToken))
			}
			return &scheduler.ListSchedulesOutput{Schedules: []schedulertypes.ScheduleSummary{{Name: sdkaws.String("a")}}, NextToken: sdkaws.String("next")}, nil
		}
		if sdkaws.ToString(input.NextToken) != "next" {
			t.Fatalf("unexpected next token on second page: %q", sdkaws.ToString(input.NextToken))
		}
		return &scheduler.ListSchedulesOutput{Schedules: []schedulertypes.ScheduleSummary{{Name: sdkaws.String("b")}}}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	removed, err := deletePendingSchedules(context.Background(), "org")
	if err != nil {
		t.Fatalf("deletePendingSchedules: %v", err)
	}
	if len(removed) != 2 || removed[0] != "a" || removed[1] != "b" {
		t.Fatalf("unexpected removed schedules: %v", removed)
	}
}
