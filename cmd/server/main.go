package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/mjones/temporal-word-game/internal/api"
	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

func main() {
	clientOptions := envconfig.MustLoadDefaultClientOptions()
	temporalClient, err := client.Dial(clientOptions)
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()

	address := env("HTTP_ADDRESS", ":8080")
	taskQueue := env("TEMPORAL_TASK_QUEUE", workflows.TaskQueue)
	temporalNamespace := clientOptions.Namespace
	if temporalNamespace == "" {
		temporalNamespace = "default"
	}
	sessionJWTSecret := os.Getenv("SESSION_JWT_SECRET")
	if sessionJWTSecret == "" {
		log.Fatal("SESSION_JWT_SECRET is required")
	}
	if _, err := bootstrap.StartDefaultCampaign(context.Background(), temporalClient, taskQueue); err != nil {
		log.Fatal(err)
	}
	log.Printf("web app listening on %s", address)
	if err := http.ListenAndServe(address, api.New(temporalClient, taskQueue, temporalNamespace, sessionJWTSecret)); err != nil {
		log.Fatal(err)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
