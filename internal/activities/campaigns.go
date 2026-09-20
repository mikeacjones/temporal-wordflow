package activities

import (
	"context"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

type Campaigns struct {
	Temporal      client.Client
	ClientOptions client.Options
	TaskQueue     string
}

func (a *Campaigns) RegisterCampaign(ctx context.Context, input workflows.RegisterCampaignActivityInput) error {
	temporalClient, closeClient, err := a.client()
	if err != nil {
		return err
	}
	defer closeClient()

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
	var view campaign.CatalogView
	return handle.Get(ctx, &view)
}

func (a *Campaigns) ResolveWordflowLevel(ctx context.Context, input workflows.ResolveWordflowLevelActivityInput) (workflows.WordflowLevelResolution, error) {
	temporalClient, closeClient, err := a.client()
	if err != nil {
		return workflows.WordflowLevelResolution{}, err
	}
	defer closeClient()

	result, err := temporalClient.QueryWorkflow(ctx, input.CampaignWorkflowID, "", workflows.QueryWordflowLevel, input.Query)
	if err != nil {
		return workflows.WordflowLevelResolution{}, err
	}
	var resolution workflows.WordflowLevelResolution
	if err := result.Get(&resolution); err != nil {
		return workflows.WordflowLevelResolution{}, err
	}
	return resolution, nil
}

func (a *Campaigns) client() (client.Client, func(), error) {
	if a.Temporal != nil {
		return a.Temporal, func() {}, nil
	}
	temporalClient, err := client.Dial(a.ClientOptions)
	if err != nil {
		return nil, nil, err
	}
	return temporalClient, temporalClient.Close, nil
}

func (a *Campaigns) taskQueue() string {
	if a.TaskQueue != "" {
		return a.TaskQueue
	}
	return workflows.TaskQueue
}
