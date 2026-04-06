package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	sdkconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/ffreis/platform-org/internal/activation"
)

var (
	loadDefaultConfigFn = sdkconfig.LoadDefaultConfig
	activateCostTagsFn  = func(ctx context.Context, cfg sdkaws.Config) error {
		return activation.Activate(ctx, costexplorer.NewFromConfig(cfg))
	}
	publishNotificationFn = func(ctx context.Context, cfg sdkaws.Config, topicARN, subject, msg string) error {
		_, err := sns.NewFromConfig(cfg).Publish(ctx, &sns.PublishInput{
			TopicArn: sdkaws.String(topicARN),
			Subject:  sdkaws.String(subject),
			Message:  sdkaws.String(msg),
		})
		return err
	}
	lambdaStartFn = func() {
		lambda.Start(handler)
	}
)

func handler(ctx context.Context, _ json.RawMessage) error {
	topicARN := os.Getenv("PLATFORM_EVENTS_TOPIC_ARN")

	// AWS_REGION is injected automatically by the Lambda runtime.
	cfg, err := loadDefaultConfigFn(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	publish := func(subject, msg string) {
		if topicARN == "" {
			return
		}
		pubErr := publishNotificationFn(ctx, cfg, topicARN, subject, msg)
		if pubErr != nil {
			log.Printf("WARN: failed to publish SNS notification: %v", pubErr)
		}
	}

	activateErr := activateCostTagsFn(ctx, cfg)
	if activateErr != nil {
		var notReady *activation.ErrNotReady
		if errors.As(activateErr, &notReady) {
			log.Printf("WARN: cost allocation tags not ready yet, will retry: %v", notReady.Missing)
			publish(
				"[platform-org] cost allocation tags not ready yet",
				fmt.Sprintf(
					"Activation deferred — AWS Cost Explorer hasn't discovered these tag keys yet: %v\n\nThe schedule will retry automatically.",
					notReady.Missing,
				),
			)
			// Return non-nil so EventBridge Scheduler retries on next attempt.
			return activateErr
		}
		log.Printf("ERROR: cost allocation tag activation failed: %v", activateErr)
		publish(
			"[platform-org] cost allocation tag activation FAILED",
			fmt.Sprintf(
				"Activation failed with error: %v\n\nManual intervention may be required: run 'platform-org activate'.",
				activateErr,
			),
		)
		return activateErr
	}

	log.Printf("cost allocation tags activated successfully: %v", activation.CostAllocationTags)
	publish(
		"[platform-org] cost allocation tags activated",
		fmt.Sprintf(
			"All cost allocation tags are now active in AWS Cost Explorer: %v\n\nCost reports by Stack, Project, Layer, Owner, and Environment are now available.",
			activation.CostAllocationTags,
		),
	)
	return nil
}

func main() {
	lambdaStartFn()
}
