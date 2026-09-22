package main

import (
	"log"
	"os"

	"github.com/mjones/temporal-word-game/internal/activities"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/worker"
)

func main() {
	temporalClient, err := client.Dial(envconfig.MustLoadDefaultClientOptions())
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()

	taskQueue := os.Getenv("TEMPORAL_TASK_QUEUE")
	if taskQueue == "" {
		taskQueue = workflows.TaskQueue
	}

	buildID := os.Getenv("TEMPORAL_WORKER_BUILD_ID")
	workerOptions := worker.Options{}
	if buildID != "" {
		workerOptions.DeploymentOptions = worker.DeploymentOptions{
			UseVersioning: true,
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: workflows.WorkerDeploymentName,
				BuildID:        buildID,
			},
		}
	}

	temporalWorker := worker.New(temporalClient, taskQueue, workerOptions)
	workflows.Register(temporalWorker, buildID != "")
	temporalWorker.RegisterActivity(&activities.Campaigns{Temporal: temporalClient, TaskQueue: taskQueue})
	temporalWorker.RegisterActivity(&activities.DailyChallenges{})
	temporalWorker.RegisterActivity(&activities.Points{Temporal: temporalClient})
	temporalWorker.RegisterActivity(&activities.Leaderboard{Temporal: temporalClient, TaskQueue: taskQueue})
	if err := temporalWorker.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}
