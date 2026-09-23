package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

func main() {
	file := flag.String("file", "", "path to a Wordflow campaign JSON file")
	taskQueue := flag.String("task-queue", workflows.TaskQueue, "Temporal task queue")
	flag.Parse()
	if *file == "" {
		log.Fatal("-file is required")
	}

	contents, err := os.ReadFile(*file)
	if err != nil {
		log.Fatal(err)
	}
	input, err := bootstrap.ParseWordflowCampaign(contents)
	if err != nil {
		log.Fatal(err)
	}

	temporalClient, err := client.Dial(envconfig.MustLoadDefaultClientOptions())
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()

	registration, err := bootstrap.StartWordflowCampaign(context.Background(), temporalClient, *taskQueue, input)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("started %s as %s\n", registration.Definition.ID, registration.WorkflowID)
}
