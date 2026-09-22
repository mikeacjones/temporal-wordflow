package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

func main() {
	date := flag.String("date", "", "Toronto calendar date in YYYY-MM-DD format")
	taskQueue := flag.String("task-queue", workflows.TaskQueue, "Temporal task queue")
	flag.Parse()
	if *date == "" {
		log.Fatal("-date is required")
	}

	temporalClient, err := client.Dial(envconfig.MustLoadDefaultClientOptions())
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()

	run, err := temporalClient.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:                       workflows.DailyWordflowChallengeWorkflowID,
		TaskQueue:                *taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, workflows.DailyWordflowChallengeWorkflowName, workflows.DailyWordflowChallengeWorkflowInput{Date: *date})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("started %s (run %s); first campaign date %s\n", run.GetID(), run.GetRunID(), *date)
}
