package workflows

import (
	"testing"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/stretchr/testify/require"
)

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
