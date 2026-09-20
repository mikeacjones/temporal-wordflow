package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/mjones/temporal-word-game/internal/api"
	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/temporalconfig"
	"github.com/mjones/temporal-word-game/internal/workflows"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

func main() {
	ctx := context.Background()
	if err := temporalconfig.LoadAPIKey(ctx); err != nil {
		log.Fatal(err)
	}
	clientOptions := envconfig.MustLoadDefaultClientOptions()
	temporalClient, err := client.Dial(clientOptions)
	if err != nil {
		log.Fatal(err)
	}

	taskQueue := env("TEMPORAL_TASK_QUEUE", workflows.TaskQueue)
	if err := bootstrap.StartDefaultCampaign(ctx, temporalClient, taskQueue); err != nil {
		log.Fatal(err)
	}
	temporalNamespace := env("TEMPORAL_WEB_UI_NAMESPACE", clientOptions.Namespace)
	handler := api.New(
		temporalClient,
		taskQueue,
		env("TEMPORAL_WEB_UI_URL", "https://cloud.temporal.io"),
		temporalNamespace,
	)
	lambda.Start(httpadapter.NewV2(handler).ProxyWithContext)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
