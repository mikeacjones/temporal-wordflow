package workflows

import (
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type CatalogWorkflowInput struct {
	InitialCampaigns []CampaignRegistration `json:"initialCampaigns,omitempty"`
	State            *CatalogState          `json:"state,omitempty"`
}

type CatalogState struct {
	Campaigns []CampaignRegistration `json:"campaigns"`
}

// CampaignRegistration is the immutable campaign snapshot published to the Catalog.
// It lets the Catalog render campaign cards and authorize level starts without
// querying every Campaign Workflow.
type CampaignRegistration struct {
	WorkflowID string              `json:"workflowId"`
	Definition campaign.Definition `json:"definition"`
	Levels     []game.Puzzle       `json:"levels"`
}

type UnregisterCampaignInput struct {
	CampaignID string `json:"campaignId"`
	WorkflowID string `json:"workflowId"`
}

func CatalogWorkflow(ctx workflow.Context, input CatalogWorkflowInput) error {
	state := input.State
	if state == nil {
		state = &CatalogState{}
		for _, registration := range input.InitialCampaigns {
			if err := validateCampaignRegistration(state, registration); err != nil {
				return err
			}
			addCampaign(state, registration)
		}
	}
	changed := workflow.NewBufferedChannel(ctx, 1)

	if err := workflow.SetQueryHandler(ctx, QueryCatalog, func(input campaign.QueryInput) (campaign.CatalogView, error) {
		// Query results are not recorded in Workflow history. Wall time keeps
		// upcoming and active cards current without mutating durable state.
		now := time.Now().UTC() //workflowcheck:ignore
		return catalogView(state, input.Player, now), nil
	}); err != nil {
		return err
	}

	if err := workflow.SetQueryHandler(ctx, QueryCatalogWordflowLevel,
		func(input WordflowLevelQuery) (WordflowLevelResolution, error) {
			now := time.Now().UTC() //workflowcheck:ignore
			return resolveCatalogWordflowLevel(state, input, now), nil
		}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateOpenCatalog,
		func(_ workflow.Context, required []CampaignRegistration) (int, error) {
			for _, registration := range required {
				addCampaign(state, registration)
			}
			changed.SendAsync(true)
			return len(state.Campaigns), nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, required []CampaignRegistration) error {
				for _, registration := range required {
					if err := validateCampaignRegistration(state, registration); err != nil {
						return err
					}
				}
				return nil
			},
		}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateRegisterCampaign,
		func(_ workflow.Context, registration CampaignRegistration) (bool, error) {
			if existing := findCampaignRegistration(state, registration.Definition.ID); existing != nil {
				return false, nil
			}
			addCampaign(state, registration)
			changed.SendAsync(true)
			return true, nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, registration CampaignRegistration) error {
				return validateCampaignRegistration(state, registration)
			},
		}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateUnregisterCampaign,
		func(_ workflow.Context, input UnregisterCampaignInput) (bool, error) {
			removed := removeCampaign(state, input)
			if removed {
				changed.SendAsync(true)
			}
			return removed, nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, input UnregisterCampaignInput) error {
				if input.CampaignID == "" || input.WorkflowID == "" {
					return temporal.NewApplicationError("campaign removal is incomplete", "invalid_campaign_removal")
				}
				return nil
			},
		}); err != nil {
		return err
	}

	for {
		var ignored bool
		changed.Receive(ctx, &ignored)
		if !workflow.GetInfo(ctx).GetContinueAsNewSuggested() &&
			!workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
			continue
		}
		if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
			return err
		}
		return workflow.NewContinueAsNewErrorWithOptions(ctx, workflow.ContinueAsNewErrorOptions{
			InitialVersioningBehavior: workflow.ContinueAsNewVersioningBehaviorAutoUpgrade,
		}, CatalogWorkflowName, CatalogWorkflowInput{State: state})
	}
}

func addCampaign(state *CatalogState, registration CampaignRegistration) {
	if findCampaignRegistration(state, registration.Definition.ID) != nil {
		return
	}
	state.Campaigns = append(state.Campaigns, registration)
}

func validateCampaignRegistration(state *CatalogState, registration CampaignRegistration) error {
	definition := registration.Definition
	if definition.ID == "" || registration.WorkflowID == "" || definition.Game.ID == "" || definition.Game.Title == "" {
		return temporal.NewApplicationError("campaign registration is incomplete", "invalid_campaign_registration")
	}
	if err := validateWordflowCampaignLevels(registration.Levels); err != nil {
		return err
	}
	if existing := findCampaignRegistration(state, definition.ID); existing != nil {
		if existing.WorkflowID != registration.WorkflowID {
			return temporal.NewApplicationError("campaign ID is already registered", "campaign_already_registered")
		}
		return nil
	}
	for _, existing := range state.Campaigns {
		if existing.Definition.Game.ID == definition.Game.ID && existing.Definition.Game != definition.Game {
			return temporal.NewApplicationError("game ID is registered with different metadata", "game_already_registered")
		}
	}
	return nil
}

func findCampaignRegistration(state *CatalogState, campaignID string) *CampaignRegistration {
	for index := range state.Campaigns {
		if state.Campaigns[index].Definition.ID == campaignID {
			return &state.Campaigns[index]
		}
	}
	return nil
}

func removeCampaign(state *CatalogState, input UnregisterCampaignInput) bool {
	for index, registration := range state.Campaigns {
		if registration.Definition.ID == input.CampaignID && registration.WorkflowID == input.WorkflowID {
			state.Campaigns = append(state.Campaigns[:index], state.Campaigns[index+1:]...)
			return true
		}
	}
	return false
}

func catalogView(state *CatalogState, player campaign.PlayerProgress, now time.Time) campaign.CatalogView {
	view := campaign.CatalogView{Campaigns: make([]campaign.View, 0, len(state.Campaigns))}
	for _, registration := range state.Campaigns {
		view.Campaigns = append(view.Campaigns, wordflowCampaignView(registration, player, now))
	}
	return view
}

func resolveCatalogWordflowLevel(state *CatalogState, input WordflowLevelQuery, now time.Time) WordflowLevelResolution {
	registration := findCampaignRegistration(state, input.CampaignID)
	if registration == nil {
		return WordflowLevelResolution{
			CampaignID: input.CampaignID,
			Reason:     "campaign is not available",
			ErrorType:  "campaign_not_found",
		}
	}
	return resolveWordflowLevel(*registration, input, now)
}
