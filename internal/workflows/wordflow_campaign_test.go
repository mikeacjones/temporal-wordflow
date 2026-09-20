package workflows

import (
	"context"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func testRegisterCampaignActivity(context.Context, RegisterCampaignActivityInput) error {
	return nil
}

func TestWordflowCampaignExposesTheCommonCampaignQueries(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(testRegisterCampaignActivity, activity.RegisterOptions{Name: ActivityRegisterCampaign})
	env.OnActivity(ActivityRegisterCampaign, mock.Anything, mock.Anything).Return(nil).Once()

	joinedAt := env.Now()
	levels := []game.Puzzle{{Level: 1, Title: "One"}, {Level: 2, Title: "Two"}}
	definition := campaign.Definition{
		ID: "test", Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		Title:  "Test campaign",
		Unlock: campaign.UnlockPolicy{InitialLevels: 1, LevelsPerInterval: 1, Interval: 24 * time.Hour},
	}
	var summary campaign.Summary
	var view campaign.View
	var resolution WordflowLevelResolution
	env.RegisterDelayedCallback(func() {
		result, err := env.QueryWorkflow(QueryCampaignSummary)
		require.NoError(t, err)
		require.NoError(t, result.Get(&summary))

		player := campaign.PlayerProgress{Campaigns: []campaign.PlayerCampaignProgress{{
			CampaignID: "test", JoinedAt: joinedAt, NextLevel: 1,
		}}}
		result, err = env.QueryWorkflow(QueryCampaignView, campaign.QueryInput{Player: player})
		require.NoError(t, err)
		require.NoError(t, result.Get(&view))
		result, err = env.QueryWorkflow(QueryWordflowLevel, WordflowLevelQuery{Player: player, Level: 1})
		require.NoError(t, err)
		require.NoError(t, result.Get(&resolution))
		env.CancelWorkflow()
	}, time.Millisecond)

	env.ExecuteWorkflow(WordflowCampaignWorkflow, WordflowCampaignWorkflowInput{
		Definition: definition, Levels: levels,
	})

	require.Equal(t, "test", summary.CampaignID)
	require.Equal(t, campaign.StatusActive, summary.Status)
	require.Equal(t, campaign.LevelAvailable, view.Levels[0].Status)
	require.Equal(t, campaign.LevelLocked, view.Levels[1].Status)
	require.True(t, resolution.Allowed)
	require.Equal(t, levels[0], resolution.Puzzle)
	env.AssertExpectations(t)
}

func TestCampaignRequirementsAndEnrollmentUnlockSchedule(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	definition := campaign.Definition{
		Requirements: campaign.Requirements{
			Campaigns: []string{"first"},
			Levels:    []campaign.RequiredLevel{{CampaignID: "second", Level: 3}},
		},
	}
	player := campaign.PlayerProgress{Campaigns: []campaign.PlayerCampaignProgress{
		{CampaignID: "first", Completed: true},
		{CampaignID: "second", CompletedLevels: 3},
	}}
	eligible, reason := campaignEligibility(definition, player, campaign.StatusActive)
	require.True(t, eligible)
	require.Empty(t, reason)

	policy := campaign.UnlockPolicy{InitialLevels: 3, LevelsPerInterval: 3, Interval: 24 * time.Hour}
	require.Equal(t, 3, unlockedWordflowLevels(policy, now, now.Add(23*time.Hour), 30))
	require.Equal(t, 6, unlockedWordflowLevels(policy, now, now.Add(24*time.Hour), 30))
	require.Equal(t, 30, unlockedWordflowLevels(policy, now, now.Add(30*24*time.Hour), 30))
}

func TestCampaignStatusUsesConfiguredWindow(t *testing.T) {
	startsAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	endsAt := startsAt.Add(48 * time.Hour)
	definition := campaign.Definition{StartsAt: &startsAt, EndsAt: &endsAt}

	require.Equal(t, campaign.StatusUpcoming, wordflowCampaignStatus(definition, startsAt.Add(-time.Second)))
	require.Equal(t, campaign.StatusActive, wordflowCampaignStatus(definition, startsAt))
	require.Equal(t, campaign.StatusEnded, wordflowCampaignStatus(definition, endsAt))
}
