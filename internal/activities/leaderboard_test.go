package activities

import (
	"context"
	"testing"

	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"
	"github.com/stretchr/testify/require"

	"go.temporal.io/sdk/client"
)

type signalWithStartClient struct {
	client.Client
	workflowID string
	signalName string
	signalArg  any
	options    client.StartWorkflowOptions
}

func (c *signalWithStartClient) SignalWithStartWorkflow(_ context.Context, workflowID, signalName string, signalArg any,
	options client.StartWorkflowOptions, _ any, _ ...any,
) (client.WorkflowRun, error) {
	c.workflowID = workflowID
	c.signalName = signalName
	c.signalArg = signalArg
	c.options = options
	return nil, nil
}

func TestPublishLeaderboardUsesSignalWithStart(t *testing.T) {
	temporalClient := &signalWithStartClient{}
	activity := &Leaderboard{Temporal: temporalClient, TaskQueue: "wordflow"}
	entry := game.LeaderboardEntry{
		PlayerID: "player", DisplayName: "Durable", LifetimePointsEarned: 42, CompletedLevels: 2, Version: 2,
	}

	require.NoError(t, activity.PublishLeaderboard(context.Background(), entry))
	require.Equal(t, workflows.LeaderboardWorkflowID, temporalClient.workflowID)
	require.Equal(t, workflows.SignalLeaderboardScore, temporalClient.signalName)
	require.Equal(t, entry, temporalClient.signalArg)
	require.Equal(t, "wordflow", temporalClient.options.TaskQueue)
}
