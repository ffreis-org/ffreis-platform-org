package cmd

import (
	"errors"
	"testing"
)

// --- isTargetsStillPresentError: remaining branches ---

func TestIsTargetsStillPresentErrorHasTargets(t *testing.T) {
	err := errors.New("the rule has targets and cannot be deleted")
	if !isTargetsStillPresentError(err) {
		t.Fatal("expected true for 'has targets' message")
	}
}

func TestIsTargetsStillPresentErrorRuleCanDeleted(t *testing.T) {
	err := errors.New("rule can only be deleted since it has associated resources")
	if !isTargetsStillPresentError(err) {
		t.Fatal("expected true for 'rule can ... deleted since it has' message")
	}
}

func TestIsTargetsStillPresentErrorNil(t *testing.T) {
	if isTargetsStillPresentError(nil) {
		t.Fatal("expected false for nil error")
	}
}

func TestIsTargetsStillPresentErrorUnrelated(t *testing.T) {
	if isTargetsStillPresentError(errors.New("access denied")) {
		t.Fatal("expected false for unrelated error")
	}
}
