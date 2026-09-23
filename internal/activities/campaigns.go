package activities

import (
	"context"
	"errors"

	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

type Campaigns struct {
	Temporal  client.Client
	Provider  *TemporalClientProvider
	TaskQueue string
}

func (a *Campaigns) RegisterCampaign(ctx context.Context, input workflows.RegisterCampaignActivityInput) error {
	temporalClient, err := activityTemporalClient(a.Temporal, a.Provider)
	if err != nil {
		return err
	}

	start := temporalClient.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
		ID:                       workflows.CatalogWorkflowID,
		TaskQueue:                a.taskQueue(),
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, workflows.CatalogWorkflowName, workflows.CatalogWorkflowInput{})
	handle, err := temporalClient.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: start,
		UpdateOptions: client.UpdateWorkflowOptions{
			UpdateID: input.UpdateID, UpdateName: workflows.UpdateRegisterCampaign,
			WaitForStage: client.WorkflowUpdateStageCompleted, Args: []any{input.Registration},
		},
	})
	if err != nil {
		return err
	}
	var added bool
	if err := handle.Get(ctx, &added); err != nil {
		var applicationError *temporal.ApplicationError
		if errors.As(err, &applicationError) {
			return temporal.NewNonRetryableApplicationError(applicationError.Message(), applicationError.Type(), err)
		}
		return err
	}
	return nil
}

func (a *Campaigns) UnregisterCampaign(ctx context.Context, input workflows.UnregisterCampaignActivityInput) error {
	temporalClient, err := activityTemporalClient(a.Temporal, a.Provider)
	if err != nil {
		return err
	}

	handle, err := temporalClient.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   workflows.CatalogWorkflowID,
		UpdateID:     input.UpdateID,
		UpdateName:   workflows.UpdateUnregisterCampaign,
		WaitForStage: client.WorkflowUpdateStageCompleted,
		Args: []any{workflows.UnregisterCampaignInput{
			CampaignID: input.CampaignID,
			WorkflowID: input.WorkflowID,
		}},
	})
	if err != nil {
		return err
	}
	var removed bool
	if err := handle.Get(ctx, &removed); err != nil {
		var applicationError *temporal.ApplicationError
		if errors.As(err, &applicationError) {
			return temporal.NewNonRetryableApplicationError(applicationError.Message(), applicationError.Type(), err)
		}
		return err
	}
	return nil
}

func (a *Campaigns) ResolveWordflowLevel(ctx context.Context, input workflows.ResolveWordflowLevelActivityInput) (workflows.WordflowLevelResolution, error) {
	temporalClient, err := activityTemporalClient(a.Temporal, a.Provider)
	if err != nil {
		return workflows.WordflowLevelResolution{}, err
	}

	result, err := temporalClient.QueryWorkflow(ctx, workflows.CatalogWorkflowID, "",
		workflows.QueryCatalogWordflowLevel, input.Query)
	if err != nil {
		return workflows.WordflowLevelResolution{}, err
	}
	var resolution workflows.WordflowLevelResolution
	if err := result.Get(&resolution); err != nil {
		return workflows.WordflowLevelResolution{}, err
	}
	return resolution, nil
}

func (a *Campaigns) taskQueue() string {
	if a.TaskQueue != "" {
		return a.TaskQueue
	}
	return workflows.TaskQueue
}
