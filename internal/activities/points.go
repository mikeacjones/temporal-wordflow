package activities

import (
	"context"
	"errors"

	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

type Points struct {
	Temporal client.Client
	Provider *TemporalClientProvider
}

func (a *Points) SpendPoints(ctx context.Context, input workflows.SpendPointsActivityInput) (workflows.SpendPointsResult, error) {
	temporalClient, err := activityTemporalClient(a.Temporal, a.Provider)
	if err != nil {
		return workflows.SpendPointsResult{}, err
	}

	handle, err := temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   input.PlayerWorkflowID,
		UpdateID:     input.UpdateID,
		UpdateName:   workflows.UpdateSpendPoints,
		WaitForStage: client.WorkflowUpdateStageCompleted,
		Args:         []any{input.Spend},
	})
	if err != nil {
		return workflows.SpendPointsResult{}, err
	}

	var result workflows.SpendPointsResult
	if err := handle.Get(ctx, &result); err != nil {
		var applicationError *temporal.ApplicationError
		if errors.As(err, &applicationError) {
			return workflows.SpendPointsResult{}, temporal.NewNonRetryableApplicationError(
				applicationError.Message(), applicationError.Type(), err,
			)
		}
		return workflows.SpendPointsResult{}, err
	}
	return result, nil
}
