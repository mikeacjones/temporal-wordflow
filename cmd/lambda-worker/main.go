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
		DeploymentName: workflows.WorkerDeploymentName,
		BuildID:        buildID,
	}, func(options *lambdaworker.Options) error {
		if options.TaskQueue == "" {
			options.TaskQueue = workflows.TaskQueue
		}
		temporalClients := activities.NewTemporalClientProvider(options.ClientOptions)
		options.OnShutdown(temporalClients.Close)
		workflows.Register(options, true)
		options.RegisterActivity(&activities.Campaigns{
			Provider:  temporalClients,
			TaskQueue: options.TaskQueue,
		})
		options.RegisterActivity(&activities.DailyChallenges{})
		options.RegisterActivity(&activities.Points{Provider: temporalClients})
		options.RegisterActivity(&activities.Leaderboard{
			Provider:  temporalClients,
			TaskQueue: options.TaskQueue,
		})
		return nil
	})
}
