package main

import (
	"context"
	"log"
	"os"

	"github.com/mjones/temporal-word-game/internal/activities"
	"github.com/mjones/temporal-word-game/internal/temporalconfig"
	"github.com/mjones/temporal-word-game/internal/workflows"

	lambdaworker "go.temporal.io/sdk/contrib/aws/lambdaworker"
	"go.temporal.io/sdk/worker"
)

func main() {
	if err := temporalconfig.LoadAPIKey(context.Background()); err != nil {
		log.Fatal(err)
	}

	buildID := os.Getenv("TEMPORAL_WORKER_BUILD_ID")
	if buildID == "" {
		buildID = "development"
	}

	lambdaworker.RunWorker(worker.WorkerDeploymentVersion{
		DeploymentName: "temporal-word-game",
		BuildID:        buildID,
	}, func(options *lambdaworker.Options) error {
		if options.TaskQueue == "" {
			options.TaskQueue = workflows.TaskQueue
		}
		workflows.Register(options, true)
		options.RegisterActivity(&activities.Campaigns{
			ClientOptions: options.ClientOptions,
			TaskQueue:     options.TaskQueue,
		})
		options.RegisterActivity(&activities.Points{ClientOptions: options.ClientOptions})
		options.RegisterActivity(&activities.Leaderboard{
			ClientOptions: options.ClientOptions,
			TaskQueue:     options.TaskQueue,
		})
		return nil
	})
}
