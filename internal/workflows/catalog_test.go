package workflows

import (
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func TestOpenCatalogReturnsCampaignsSeededAtStart(t *testing.T) {
	registration := campaign.Registration{
		CampaignID: "temporal-foundations",
		WorkflowID: "wordflow-campaign/temporal-foundations",
		Game:       campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
	}
	var view campaign.CatalogView
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateOpenCatalog, "open", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				view = result.(campaign.CatalogView)
			},
		}, []campaign.Registration{})
	}, time.Millisecond)
	env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Millisecond)

	env.ExecuteWorkflow(CatalogWorkflow, CatalogWorkflowInput{
		InitialCampaigns: []campaign.Registration{registration},
	})

	require.Equal(t, []campaign.GameSummary{registration.Game}, view.Games)
	require.Equal(t, []campaign.Registration{registration}, view.Campaigns)
}

func TestOpenCatalogAddsRequiredCampaignToExistingState(t *testing.T) {
	registration := campaign.Registration{
		CampaignID: "temporal-foundations",
		WorkflowID: "wordflow-campaign/temporal-foundations",
		Game:       campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
	}
	var view campaign.CatalogView
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateOpenCatalog, "open", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				view = result.(campaign.CatalogView)
			},
		}, []campaign.Registration{registration})
	}, time.Millisecond)
	env.RegisterDelayedCallback(env.CancelWorkflow, 2*time.Millisecond)

	env.ExecuteWorkflow(CatalogWorkflow, CatalogWorkflowInput{})

	require.Equal(t, []campaign.Registration{registration}, view.Campaigns)
}

func TestCampaignRegistrationIsNaturallyIdempotent(t *testing.T) {
	registration := campaign.Registration{
		CampaignID: "temporal-foundations",
		WorkflowID: "wordflow-campaign/temporal-foundations",
		Game:       campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
	}
	state := &CatalogState{
		Games:     []campaign.GameSummary{registration.Game},
		Campaigns: []campaign.Registration{registration},
	}

	require.NoError(t, validateCampaignRegistration(state, registration))
	conflict := registration
	conflict.WorkflowID = "wordflow-campaign/other"
	require.ErrorContains(t, validateCampaignRegistration(state, conflict), "already registered")
}
