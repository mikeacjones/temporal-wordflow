package workflows

import (
	"context"
	"strings"
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

func testUnregisterCampaignActivity(context.Context, UnregisterCampaignActivityInput) error {
	return nil
}

func TestWordflowCampaignExposesTheCommonCampaignQueries(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(testRegisterCampaignActivity, activity.RegisterOptions{Name: ActivityRegisterCampaign})
	var registrationUpdateID string
	env.OnActivity(ActivityRegisterCampaign, mock.Anything, mock.Anything).
		Run(func(arguments mock.Arguments) {
			registrationUpdateID = arguments.Get(1).(RegisterCampaignActivityInput).UpdateID
		}).Return(nil).Once()

	joinedAt := env.Now()
	levels := []game.Puzzle{
		{Level: 1, Title: "One"},
		{Level: 2, Title: "Two"},
		{Level: 3, Title: "Three"},
		{Level: 4, Title: "Four"},
	}
	definition := campaign.Definition{
		ID: "test", Kind: campaign.KindDailyChallenge,
		Game:   campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		Title:  "Test campaign",
		Unlock: campaign.UnlockPolicy{InitialLevels: 3, LevelsPerInterval: 1, Interval: 24 * time.Hour},
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
	require.Equal(t, campaign.KindDailyChallenge, summary.Kind)
	require.Equal(t, campaign.KindDailyChallenge, view.Kind)
	require.Equal(t, campaign.StatusActive, summary.Status)
	require.Equal(t, campaign.LevelAvailable, view.Levels[0].Status)
	require.Equal(t, campaign.LevelUnlocked, view.Levels[1].Status)
	require.Equal(t, campaign.LevelUnlocked, view.Levels[2].Status)
	require.Equal(t, campaign.LevelLocked, view.Levels[3].Status)
	require.True(t, resolution.Allowed)
	require.Equal(t, levels[0], resolution.Puzzle)
	require.True(t, strings.HasPrefix(registrationUpdateID, "register/test/"))
	env.AssertExpectations(t)
}

func TestWordflowCampaignQueriesUseWallClock(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(testRegisterCampaignActivity, activity.RegisterOptions{Name: ActivityRegisterCampaign})
	env.OnActivity(ActivityRegisterCampaign, mock.Anything, mock.Anything).Return(nil).Once()

	wallNow := time.Now().UTC()
	env.SetStartTime(wallNow.Add(-48 * time.Hour))
	startsAt := wallNow.Add(-time.Hour)
	endsAt := wallNow.Add(time.Hour)
	joinedAt := wallNow.Add(-25 * time.Hour)
	levels := []game.Puzzle{
		{Level: 1, Title: "One"},
		{Level: 2, Title: "Two"},
	}
	definition := campaign.Definition{
		ID: "wall-clock", Game: campaign.GameSummary{ID: campaign.GameWordflow},
		StartsAt: &startsAt, EndsAt: &endsAt,
		Unlock: campaign.UnlockPolicy{InitialLevels: 1, LevelsPerInterval: 1, Interval: 24 * time.Hour},
	}
	player := campaign.PlayerProgress{Campaigns: []campaign.PlayerCampaignProgress{{
		CampaignID: definition.ID, JoinedAt: joinedAt, NextLevel: 2, CompletedLevels: 1,
	}}}

	var summary campaign.Summary
	var view campaign.View
	var resolution WordflowLevelResolution
	env.RegisterDelayedCallback(func() {
		result, err := env.QueryWorkflow(QueryCampaignSummary)
		require.NoError(t, err)
		require.NoError(t, result.Get(&summary))

		result, err = env.QueryWorkflow(QueryCampaignView, campaign.QueryInput{Player: player})
		require.NoError(t, err)
		require.NoError(t, result.Get(&view))

		result, err = env.QueryWorkflow(QueryWordflowLevel, WordflowLevelQuery{Player: player, Level: 2})
		require.NoError(t, err)
		require.NoError(t, result.Get(&resolution))
		env.CancelWorkflow()
	}, time.Millisecond)

	env.ExecuteWorkflow(WordflowCampaignWorkflow, WordflowCampaignWorkflowInput{
		Definition: definition, Levels: levels,
	})

	require.Equal(t, campaign.StatusActive, summary.Status)
	require.Equal(t, campaign.LevelAvailable, view.Levels[1].Status)
	require.True(t, resolution.Allowed)
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

func TestSingleAttemptCampaignLocksAfterTimeout(t *testing.T) {
	definition := campaign.Definition{ID: "daily", SingleAttempt: true}
	player := campaign.PlayerProgress{Campaigns: []campaign.PlayerCampaignProgress{{
		CampaignID: "daily", NextLevel: 2, CompletedLevels: 1, Failed: true,
	}}}

	eligible, reason := campaignEligibility(definition, player, campaign.StatusActive)
	require.False(t, eligible)
	require.Equal(t, "Today's daily challenge attempt is over.", reason)
}

func TestCampaignStatusUsesConfiguredWindow(t *testing.T) {
	startsAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	endsAt := startsAt.Add(48 * time.Hour)
	definition := campaign.Definition{StartsAt: &startsAt, EndsAt: &endsAt}

	require.Equal(t, campaign.StatusUpcoming, wordflowCampaignStatus(definition, startsAt.Add(-time.Second)))
	require.Equal(t, campaign.StatusActive, wordflowCampaignStatus(definition, startsAt))
	require.Equal(t, campaign.StatusEnded, wordflowCampaignStatus(definition, endsAt))
}

func TestCampaignUnregistersAndCompletesAtItsEndTime(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(testRegisterCampaignActivity, activity.RegisterOptions{Name: ActivityRegisterCampaign})
	env.RegisterActivityWithOptions(testUnregisterCampaignActivity, activity.RegisterOptions{Name: ActivityUnregisterCampaign})
	env.OnActivity(ActivityRegisterCampaign, mock.Anything, mock.Anything).Return(nil).Once()

	endsAt := env.Now().Add(time.Minute)
	var removal UnregisterCampaignActivityInput
	env.OnActivity(ActivityUnregisterCampaign, mock.Anything, mock.Anything).
		Run(func(arguments mock.Arguments) {
			removal = arguments.Get(1).(UnregisterCampaignActivityInput)
		}).Return(nil).Once()

	env.ExecuteWorkflow(WordflowCampaignWorkflow, WordflowCampaignWorkflowInput{
		Definition: campaign.Definition{
			ID: "expiring", EndsAt: &endsAt,
			Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		},
		Levels: []game.Puzzle{{Level: 1, Title: "One", Letters: "ONE"}},
	})

	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, "expiring", removal.CampaignID)
	require.NotEmpty(t, removal.WorkflowID)
	env.AssertExpectations(t)
}

func TestCampaignRejectsMoreThanEightLetters(t *testing.T) {
	err := validateWordflowCampaignLevels([]game.Puzzle{{Level: 1, Letters: "ABCDEFGHI"}})
	require.ErrorContains(t, err, "more than 8 letters")
}
