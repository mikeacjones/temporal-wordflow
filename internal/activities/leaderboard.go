package activities

import (
	"context"

	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

type Leaderboard struct {
	Temporal  client.Client
	Provider  *TemporalClientProvider
	TaskQueue string
}

func (a *Leaderboard) PublishLeaderboard(ctx context.Context, entry game.LeaderboardEntry) error {
	temporalClient, err := activityTemporalClient(a.Temporal, a.Provider)
	if err != nil {
		return err
	}

	taskQueue := a.TaskQueue
	if taskQueue == "" {
		taskQueue = workflows.TaskQueue
	}
	_, err = temporalClient.SignalWithStartWorkflow(ctx, workflows.LeaderboardWorkflowID,
		workflows.SignalLeaderboardScore, entry, client.StartWorkflowOptions{
			ID:                       workflows.LeaderboardWorkflowID,
			TaskQueue:                taskQueue,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
			WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		}, workflows.LeaderboardWorkflowName, workflows.LeaderboardWorkflowInput{})
	return err
}
