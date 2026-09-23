package workflows

import (
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func testCampaignRegistration(id string) CampaignRegistration {
	return CampaignRegistration{
		WorkflowID: WordflowCampaignWorkflowID(id),
		Definition: campaign.Definition{
			ID: id,
			Game: campaign.GameSummary{
				ID: campaign.GameWordflow, Title: "Wordflow",
			},
			Title:  "Test campaign",
			Unlock: campaign.UnlockPolicy{InitialLevels: 1},
		},
		Levels: []game.Puzzle{{Level: 1, Title: "One", Letters: "ONE"}},
	}
}

func TestCatalogQueryBuildsCampaignViewsFromRegisteredSnapshots(t *testing.T) {
	registration := testCampaignRegistration("temporal-foundations")
	var view campaign.CatalogView
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() {
		result, err := env.QueryWorkflow(QueryCatalog, campaign.QueryInput{})
		require.NoError(t, err)
		require.NoError(t, result.Get(&view))
	}, time.Millisecond)
	env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Millisecond)

	env.ExecuteWorkflow(CatalogWorkflow, CatalogWorkflowInput{
		InitialCampaigns: []CampaignRegistration{registration},
	})

	require.Len(t, view.Campaigns, 1)
	require.Equal(t, registration.Definition.ID, view.Campaigns[0].CampaignID)
	require.Equal(t, registration.WorkflowID, view.Campaigns[0].WorkflowID)
	require.Equal(t, campaign.LevelAvailable, view.Campaigns[0].Levels[0].Status)
}

func TestOpenCatalogAddsRequiredCampaignToExistingState(t *testing.T) {
	registration := testCampaignRegistration("temporal-foundations")
	var campaignCount int
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateOpenCatalog, "open", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				campaignCount = result.(int)
			},
		}, []CampaignRegistration{registration})
	}, time.Millisecond)
	env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Millisecond)

	env.ExecuteWorkflow(CatalogWorkflow, CatalogWorkflowInput{})

	require.Equal(t, 1, campaignCount)
}

func TestCampaignRegistrationIsNaturallyIdempotent(t *testing.T) {
	registration := testCampaignRegistration("temporal-foundations")
	state := &CatalogState{Campaigns: []CampaignRegistration{registration}}

	require.NoError(t, validateCampaignRegistration(state, registration))
	conflict := registration
	conflict.WorkflowID = "wordflow-campaign/other"
	require.ErrorContains(t, validateCampaignRegistration(state, conflict), "already registered")
}

func TestCatalogEvictsOnlyTheMatchingCampaignExecution(t *testing.T) {
	registration := testCampaignRegistration("daily")
	state := &CatalogState{Campaigns: []CampaignRegistration{registration}}

	require.False(t, removeCampaign(state, UnregisterCampaignInput{
		CampaignID: "daily", WorkflowID: "wordflow-campaign/stale",
	}))
	require.True(t, removeCampaign(state, UnregisterCampaignInput{
		CampaignID: "daily", WorkflowID: registration.WorkflowID,
	}))
	require.Empty(t, state.Campaigns)
}

func TestCatalogAuthorizesLevelsFromItsRegisteredSnapshot(t *testing.T) {
	registration := testCampaignRegistration("daily")
	state := &CatalogState{Campaigns: []CampaignRegistration{registration}}

	result := resolveCatalogWordflowLevel(state, WordflowLevelQuery{
		CampaignID: "daily", Level: 1,
	}, time.Now())
	require.True(t, result.Allowed)
	require.Equal(t, registration.Levels[0], result.Puzzle)

	missing := resolveCatalogWordflowLevel(state, WordflowLevelQuery{
		CampaignID: "missing", Level: 1,
	}, time.Now())
	require.False(t, missing.Allowed)
	require.Equal(t, "campaign_not_found", missing.ErrorType)
}
