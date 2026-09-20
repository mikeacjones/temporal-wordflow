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

	localWorker := worker.New(temporalClient, taskQueue, worker.Options{})
	workflows.Register(localWorker, false)
	localWorker.RegisterActivity(&activities.Campaigns{Temporal: temporalClient, TaskQueue: taskQueue})
	localWorker.RegisterActivity(&activities.Points{Temporal: temporalClient})
	localWorker.RegisterActivity(&activities.Leaderboard{Temporal: temporalClient, TaskQueue: taskQueue})
	if err := localWorker.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}
