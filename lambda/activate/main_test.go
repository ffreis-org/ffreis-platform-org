package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	sdkconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ffreis/platform-org/internal/activation"
)

const (
	testPlatformEventsTopicARN = "arn:aws:sns:us-east-1:123456789012:platform-events"
	testLambdaUnexpectedLogsf  = "unexpected logs: %s"
	testLambdaActivationFailed = "activation failed"
)

func stubLambdaConfig() sdkaws.Config {
	return sdkaws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
	}
}

func resetLambdaActivateHooks(t *testing.T) {
	oldLoad := loadDefaultConfigFn
	oldActivate := activateCostTagsFn
	oldPublish := publishNotificationFn
	oldStart := lambdaStartFn
	t.Cleanup(func() {
		loadDefaultConfigFn = oldLoad
		activateCostTagsFn = oldActivate
		publishNotificationFn = oldPublish
		lambdaStartFn = oldStart
	})
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

func TestHandlerReturnsConfigError(t *testing.T) {
	resetLambdaActivateHooks(t)
	loadDefaultConfigFn = func(context.Context, ...func(*sdkconfig.LoadOptions) error) (sdkaws.Config, error) {
		return sdkaws.Config{}, errors.New("boom")
	}

	err := handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "load AWS config: boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandlerReturnsNotReadyAndPublishesRetryMessage(t *testing.T) {
	resetLambdaActivateHooks(t)
	logs := captureLogs(t)
	t.Setenv("PLATFORM_EVENTS_TOPIC_ARN", testPlatformEventsTopicARN)
	loadDefaultConfigFn = func(context.Context, ...func(*sdkconfig.LoadOptions) error) (sdkaws.Config, error) {
		return stubLambdaConfig(), nil
	}
	activateCostTagsFn = func(context.Context, sdkaws.Config) error {
		return &activation.ErrNotReady{Missing: []string{"Project", "Stack"}}
	}
	called := false
	publishNotificationFn = func(_ context.Context, _ sdkaws.Config, topicARN, subject, msg string) error {
		called = true
		if topicARN == "" || !strings.Contains(subject, "not ready yet") || !strings.Contains(msg, "Project") {
			t.Fatalf("unexpected publish payload: topic=%q subject=%q msg=%q", topicARN, subject, msg)
		}
		return nil
	}

	err := handler(context.Background(), nil)
	var notReady *activation.ErrNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("expected ErrNotReady, got %v", err)
	}
	if !called {
		t.Fatal("expected not-ready notification publish")
	}
	if !strings.Contains(logs.String(), "cost allocation tags not ready yet") {
		t.Fatalf(testLambdaUnexpectedLogsf, logs.String())
	}
}

func TestHandlerReturnsFailureAndPublishesFailureMessage(t *testing.T) {
	resetLambdaActivateHooks(t)
	logs := captureLogs(t)
	t.Setenv("PLATFORM_EVENTS_TOPIC_ARN", testPlatformEventsTopicARN)
	loadDefaultConfigFn = func(context.Context, ...func(*sdkconfig.LoadOptions) error) (sdkaws.Config, error) {
		return stubLambdaConfig(), nil
	}
	activateCostTagsFn = func(context.Context, sdkaws.Config) error {
		return errors.New(testLambdaActivationFailed)
	}
	called := false
	publishNotificationFn = func(_ context.Context, _ sdkaws.Config, _ string, subject, msg string) error {
		called = true
		if !strings.Contains(subject, "FAILED") || !strings.Contains(msg, testLambdaActivationFailed) {
			t.Fatalf("unexpected publish payload: subject=%q msg=%q", subject, msg)
		}
		return nil
	}

	err := handler(context.Background(), nil)
	if err == nil || err.Error() != testLambdaActivationFailed {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected failure notification publish")
	}
	if !strings.Contains(logs.String(), testLambdaActivationFailed) {
		t.Fatalf(testLambdaUnexpectedLogsf, logs.String())
	}
}

func TestHandlerSucceedsWithoutTopic(t *testing.T) {
	resetLambdaActivateHooks(t)
	logs := captureLogs(t)
	t.Setenv("PLATFORM_EVENTS_TOPIC_ARN", "")
	loadDefaultConfigFn = func(context.Context, ...func(*sdkconfig.LoadOptions) error) (sdkaws.Config, error) {
		return stubLambdaConfig(), nil
	}
	activateCostTagsFn = func(context.Context, sdkaws.Config) error { return nil }
	publishNotificationFn = func(context.Context, sdkaws.Config, string, string, string) error {
		t.Fatal("publish must not be called when topic ARN is unset")
		return nil
	}

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(logs.String(), "activated successfully") {
		t.Fatalf(testLambdaUnexpectedLogsf, logs.String())
	}
}

func TestHandlerIgnoresPublishErrorOnSuccess(t *testing.T) {
	resetLambdaActivateHooks(t)
	logs := captureLogs(t)
	t.Setenv("PLATFORM_EVENTS_TOPIC_ARN", testPlatformEventsTopicARN)
	loadDefaultConfigFn = func(context.Context, ...func(*sdkconfig.LoadOptions) error) (sdkaws.Config, error) {
		return stubLambdaConfig(), nil
	}
	activateCostTagsFn = func(context.Context, sdkaws.Config) error { return nil }
	publishNotificationFn = func(context.Context, sdkaws.Config, string, string, string) error {
		return errors.New("publish failed")
	}

	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(logs.String(), "failed to publish SNS notification") {
		t.Fatalf(testLambdaUnexpectedLogsf, logs.String())
	}
}

func TestMainStartsLambda(t *testing.T) {
	resetLambdaActivateHooks(t)
	called := false
	lambdaStartFn = func() {
		called = true
	}

	main()

	if !called {
		t.Fatal("expected main to start the lambda runtime")
	}
}
