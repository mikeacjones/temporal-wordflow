package workflows

import (
	"github.com/mjones/temporal-word-game/internal/campaign"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type CatalogWorkflowInput struct {
	State *CatalogState `json:"state,omitempty"`
}

type CatalogState struct {
	Games               []campaign.GameSummary  `json:"games"`
	Campaigns           []campaign.Registration `json:"campaigns"`
	EventsSinceContinue int                     `json:"eventsSinceContinue"`
	ContinueAfterEvents int                     `json:"continueAfterEvents"`
}

func CatalogWorkflow(ctx workflow.Context, input CatalogWorkflowInput) error {
	state := input.State
	if state == nil {
		state = &CatalogState{ContinueAfterEvents: defaultContinueAfterEvents}
	}
	if state.ContinueAfterEvents == 0 {
		state.ContinueAfterEvents = defaultContinueAfterEvents
	}
	changed := workflow.NewBufferedChannel(ctx, 1)

	if err := workflow.SetQueryHandler(ctx, QueryCatalog, func() (campaign.CatalogView, error) {
		return catalogView(state), nil
	}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateRegisterCampaign,
		func(_ workflow.Context, registration campaign.Registration) (campaign.CatalogView, error) {
			if existing := findCampaignRegistration(state, registration.CampaignID); existing != nil {
				return catalogView(state), nil
			}
			if findGame(state, registration.Game.ID) == nil {
				state.Games = append(state.Games, registration.Game)
			}
			state.Campaigns = append(state.Campaigns, registration)
			state.EventsSinceContinue++
			changed.SendAsync(true)
			return catalogView(state), nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, registration campaign.Registration) error {
				return validateCampaignRegistration(state, registration)
			},
		}); err != nil {
		return err
	}

	for {
		var ignored bool
		changed.Receive(ctx, &ignored)
		if state.EventsSinceContinue < state.ContinueAfterEvents &&
			!workflow.GetInfo(ctx).GetContinueAsNewSuggested() &&
			!workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
			continue
		}
		if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
			return err
		}
		state.EventsSinceContinue = 0
		return continueCatalogAsNew(ctx, state)
	}
}

func validateCampaignRegistration(state *CatalogState, registration campaign.Registration) error {
	if registration.CampaignID == "" || registration.WorkflowID == "" || registration.Game.ID == "" || registration.Game.Title == "" {
		return temporal.NewApplicationError("campaign registration is incomplete", "invalid_campaign_registration")
	}
	if existing := findCampaignRegistration(state, registration.CampaignID); existing != nil {
		if *existing != registration {
			return temporal.NewApplicationError("campaign ID is already registered", "campaign_already_registered")
		}
		return nil
	}
	if existing := findGame(state, registration.Game.ID); existing != nil && *existing != registration.Game {
		return temporal.NewApplicationError("game ID is registered with different metadata", "game_already_registered")
	}
	return nil
}

func findCampaignRegistration(state *CatalogState, campaignID string) *campaign.Registration {
	for index := range state.Campaigns {
		if state.Campaigns[index].CampaignID == campaignID {
			return &state.Campaigns[index]
		}
	}
	return nil
}

func findGame(state *CatalogState, gameID string) *campaign.GameSummary {
	for index := range state.Games {
		if state.Games[index].ID == gameID {
			return &state.Games[index]
		}
	}
	return nil
}

func catalogView(state *CatalogState) campaign.CatalogView {
	return campaign.CatalogView{
		Games:     append([]campaign.GameSummary(nil), state.Games...),
		Campaigns: append([]campaign.Registration(nil), state.Campaigns...),
	}
}

func continueCatalogAsNew(ctx workflow.Context, state *CatalogState) error {
	options := workflow.ContinueAsNewErrorOptions{}
	if workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
		options.InitialVersioningBehavior = workflow.ContinueAsNewVersioningBehaviorAutoUpgrade
	}
	return workflow.NewContinueAsNewErrorWithOptions(ctx, options, CatalogWorkflowName, CatalogWorkflowInput{State: state})
}
