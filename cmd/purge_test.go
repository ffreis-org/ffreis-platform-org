package cmd

import (
	"errors"
	"regexp"
	"testing"
)

const (
	testECSTaskDefinitionType = "AWS::ECS::TaskDefinition"
	testECSTaskDefinitionARN  = "arn:aws:ecs:us-east-1:123456789012:task-definition/name:42"
)

func TestPurgeClientTokenUsesAllowedCharacters(t *testing.T) {
	token := purgeClientToken(testECSTaskDefinitionType, testECSTaskDefinitionARN)

	if matched, err := regexp.MatchString(`^[-A-Za-z0-9+/=]+$`, token); err != nil {
		t.Fatalf("MatchString error: %v", err)
	} else if !matched {
		t.Fatalf("token %q does not match allowed pattern", token)
	}
}

func TestPurgeClientTokenIsStable(t *testing.T) {
	first := purgeClientToken(testECSTaskDefinitionType, testECSTaskDefinitionARN)
	second := purgeClientToken(testECSTaskDefinitionType, testECSTaskDefinitionARN)

	if first != second {
		t.Fatalf("tokens differ: %q != %q", first, second)
	}
}

func TestClassifyPurgeDeleteError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want purgeFailureDisposition
	}{
		{
			name: "gone by not found",
			err:  errors.New("delete failed: Resource of type '" + testECSTaskDefinitionType + "' with identifier 'foo' was not found"),
			want: purgeFailureGone,
		},
		{
			name: "gone by does not exist",
			err:  errors.New("delete failed: The StaticIp does not exist: ip-123"),
			want: purgeFailureGone,
		},
		{
			name: "manual by unsupported action",
			err:  errors.New("UnsupportedActionException: Resource type AWS::Foo does not support DELETE action"),
			want: purgeFailureManual,
		},
		{
			name: "manual by managed rule",
			err:  errors.New("delete failed: foo is a managed rule. Set 'force' parameter to true to override."),
			want: purgeFailureManual,
		},
		{
			name: "blocked by dependencies",
			err:  errors.New("delete failed: The routeTable 'rtb-123' has dependencies and cannot be deleted."),
			want: purgeFailureBlocked,
		},
		{
			name: "retryable throttling",
			err:  errors.New("operation error CloudControl: DeleteResource, exceeded maximum number of attempts, 3, ThrottlingException: Rate exceeded"),
			want: purgeFailureRetryable,
		},
		{
			name: "fatal dependency error",
			err:  errors.New("delete failed: something unexpected happened"),
			want: purgeFailureFatal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPurgeDeleteError(tc.err); got != tc.want {
				t.Fatalf("classifyPurgeDeleteError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
