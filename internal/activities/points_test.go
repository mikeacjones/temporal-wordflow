package activities

import (
	"context"
	"errors"
	"testing"

	"github.com/mjones/temporal-word-game/internal/workflows"
	"github.com/stretchr/testify/require"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

type updateClient struct {
	client.Client
	options client.UpdateWorkflowOptions
	handle  client.WorkflowUpdateHandle
}

func (c *updateClient) UpdateWorkflow(_ context.Context, options client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error) {
	c.options = options
	return c.handle, nil
}

type updateHandle struct {
	result workflows.SpendPointsResult
	err    error
}

func (h updateHandle) WorkflowID() string { return "player/test-player" }
func (h updateHandle) RunID() string      { return "run" }
func (h updateHandle) UpdateID() string   { return "spend/game/test-player/1/hint" }
func (h updateHandle) Get(_ context.Context, value any) error {
	if h.err != nil {
		return h.err
	}
	*(value.(*workflows.SpendPointsResult)) = h.result
	return nil
}

func TestSpendPointsUsesTemporalUpdateID(t *testing.T) {
	temporalClient := &updateClient{handle: updateHandle{
		result: workflows.SpendPointsResult{Spent: 10, Remaining: 30},
	}}
	activity := &Points{Temporal: temporalClient}
	input := workflows.SpendPointsActivityInput{
		PlayerWorkflowID: "player/test-player",
		UpdateID:         "spend/game/test-player/1/hint",
		Spend: workflows.SpendPointsInput{
			LevelWorkflowID: "wordflow-level/test-player/campaign/1",
			Amount:          10,
			Reason:          "wordflow/letter-hint",
		},
	}

	result, err := activity.SpendPoints(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, workflows.SpendPointsResult{Spent: 10, Remaining: 30}, result)
	require.Equal(t, input.UpdateID, temporalClient.options.UpdateID)
	require.Equal(t, workflows.UpdateSpendPoints, temporalClient.options.UpdateName)
	require.Equal(t, client.WorkflowUpdateStageCompleted, temporalClient.options.WaitForStage)
}

func TestSpendPointsDoesNotRetryPlayerRejection(t *testing.T) {
	temporalClient := &updateClient{handle: updateHandle{
		err: temporal.NewApplicationError("not enough points", "insufficient_points"),
	}}
	activity := &Points{Temporal: temporalClient}

	_, err := activity.SpendPoints(context.Background(), workflows.SpendPointsActivityInput{
		PlayerWorkflowID: "player/test-player",
		UpdateID:         "spend/game/test-player/1/hint",
		Spend: workflows.SpendPointsInput{
			LevelWorkflowID: "wordflow-level/test-player/campaign/1", Amount: 10, Reason: "wordflow/letter-hint",
		},
	})
	require.Error(t, err)

	var applicationError *temporal.ApplicationError
	require.True(t, errors.As(err, &applicationError))
	require.True(t, applicationError.NonRetryable())
	require.Equal(t, "insufficient_points", applicationError.Type())
}
