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
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func testPublishLeaderboardActivity(context.Context, game.LeaderboardEntry) error {
	return nil
}

func testResolveWordflowLevelActivity(context.Context, ResolveWordflowLevelActivityInput) (WordflowLevelResolution, error) {
	return WordflowLevelResolution{}, nil
}

func waitingWordflowLevelWorkflow(ctx workflow.Context, input WordflowLevelWorkflowInput) (campaign.LevelResult, error) {
	if err := workflow.Sleep(ctx, 100*time.Millisecond); err != nil {
		return campaign.LevelResult{}, err
	}
	return campaign.LevelResult{
		GameID: campaign.GameWordflow, CampaignID: input.CampaignID,
		Level: input.Puzzle.Level, Attempts: 4, CompletedAt: workflow.Now(ctx),
		Awards: []campaign.PointAward{{Description: "Wordflow score", Points: 20}},
	}, nil
}

func TestAuthenticationClaimsUsernameAndValidatesExistingPlayer(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	state := &PlayerState{PlayerID: "alice"}
	var registered, loggedIn game.PlayerView

	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateAuthenticatePlayer, "register", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				registered = result.(game.PlayerView)
			},
		}, AuthenticatePlayerInput{
			DisplayName: "Alice", PasswordHash: "correct", Register: true,
		})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateAuthenticatePlayer, "wrong-password", &testsuite.TestUpdateCallback{
			OnComplete: func(_ any, err error) { require.Error(t, err) },
		}, AuthenticatePlayerInput{PasswordHash: "wrong"})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SetContinueAsNewSuggested(true)
		env.UpdateWorkflow(UpdateAuthenticatePlayer, "login", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				loggedIn = result.(game.PlayerView)
			},
		}, AuthenticatePlayerInput{PasswordHash: "correct"})
	}, 3*time.Millisecond)

	env.ExecuteWorkflow(PlayerWorkflow, PlayerWorkflowInput{State: state})

	require.Equal(t, "alice", registered.PlayerID)
	require.Equal(t, "Alice", registered.DisplayName)
	require.Equal(t, registered.DisplayName, loggedIn.DisplayName)
	var continueAsNew *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &continueAsNew)
	var nextRun PlayerWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(continueAsNew.Input, &nextRun))
	require.Equal(t, "correct", nextRun.State.PasswordHash)
}

func TestLoginCannotClaimMissingPlayer(t *testing.T) {
	err := validateCredentials(&PlayerState{}, AuthenticatePlayerInput{PasswordHash: "hash"})
	require.ErrorContains(t, err, "account does not exist")
}

func TestPlayerRemainsResponsiveWhileLevelChildRuns(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(waitingWordflowLevelWorkflow, workflow.RegisterOptions{Name: WordflowLevelWorkflowName})
	env.RegisterActivityWithOptions(testResolveWordflowLevelActivity, activity.RegisterOptions{Name: ActivityResolveWordflowLevel})
	env.RegisterActivityWithOptions(testPublishLeaderboardActivity, activity.RegisterOptions{Name: ActivityPublishLeaderboard})

	puzzle := testPuzzle(t, 1)
	env.OnActivity(ActivityResolveWordflowLevel, mock.Anything, mock.Anything).Return(WordflowLevelResolution{
		Allowed: true, CampaignID: "temporal-foundations", TotalLevels: 30, Puzzle: puzzle,
	}, nil).Once()

	gameID := WordflowLevelWorkflowID("player", "temporal-foundations", 1)
	state := &PlayerState{PlayerID: "player", Points: 25}
	var sessionView game.PlayerView
	var spendResult SpendPointsResult

	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateStartLevel, "start", &testsuite.TestUpdateCallback{
			OnComplete: func(_ any, err error) { require.NoError(t, err) },
		}, StartLevelInput{CampaignID: "temporal-foundations", Level: 1})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		result, err := env.QueryWorkflow(QueryPlayerState)
		require.NoError(t, err)
		require.NoError(t, result.Get(&sessionView))
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SetContinueAsNewSuggested(true)
		env.UpdateWorkflow(UpdateSpendPoints, "spend", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				spendResult = result.(SpendPointsResult)
			},
		}, SpendPointsInput{LevelWorkflowID: gameID, Amount: 10, Reason: "wordflow/letter-hint"})
	}, 3*time.Millisecond)

	env.ExecuteWorkflow(PlayerWorkflow, PlayerWorkflowInput{State: state})

	require.NotNil(t, sessionView.ActiveGame)
	require.Equal(t, "temporal-foundations", sessionView.ActiveGame.CampaignID)
	require.Equal(t, SpendPointsResult{Spent: 10, Remaining: 15}, spendResult)
	var continueAsNew *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &continueAsNew)
	var nextRun PlayerWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(continueAsNew.Input, &nextRun))
	require.Nil(t, nextRun.State.ActiveGame)
	require.Equal(t, 35, nextRun.State.Points)
	require.Equal(t, 20, nextRun.State.LifetimePointsEarned)
	require.Len(t, nextRun.State.CompletedLevels, 1)
	require.Equal(t, 2, nextRun.State.Campaigns[0].NextLevel)
	env.AssertExpectations(t)
}

func TestCompletionAdvancesCampaignAndAwardsConfiguredPoints(t *testing.T) {
	completedAt := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	state := &PlayerState{
		PlayerID: "player",
		Campaigns: []campaign.PlayerCampaignProgress{{
			CampaignID: "campaign", NextLevel: 5, TotalLevels: 5, CompletedLevels: 4,
		}},
		ActiveGame:    &game.ActiveGame{CampaignID: "campaign", Level: 5, TotalLevels: 5},
		CurrentStreak: 2, BestStreak: 2,
		LastCompletionDay: dayKey(completedAt.AddDate(0, 0, -1)),
	}

	applied := applyLevelResult(state, campaign.LevelResult{
		GameID: campaign.GameWordflow, CampaignID: "campaign", Level: 5,
		Attempts: 8, CompletedAt: completedAt,
		Awards: []campaign.PointAward{
			{Description: "Wordflow score", Points: 32},
			{Description: "Level milestone", Points: 50},
			{Description: "Test Event", Points: 25},
		},
	})

	require.True(t, applied)
	require.Nil(t, state.ActiveGame)
	require.Equal(t, 6, state.Campaigns[0].NextLevel)
	require.True(t, state.Campaigns[0].Completed)
	require.Equal(t, 3, state.CurrentStreak)
	require.Equal(t, 107, state.Points)
	require.Equal(t, 107, state.LifetimePointsEarned)
	require.Equal(t, 107, state.CompletedLevels[0].Points)
	require.Len(t, state.Rewards, 3)
	require.False(t, applyLevelResult(state, campaign.LevelResult{
		CampaignID: "campaign", Level: 5, CompletedAt: completedAt,
	}))
}

func TestSpendPointsValidation(t *testing.T) {
	state := &PlayerState{
		Points:     25,
		ActiveGame: &game.ActiveGame{WorkflowID: "wordflow-level/player/campaign/1"},
	}

	require.NoError(t, validateSpendPoints(state, SpendPointsInput{
		LevelWorkflowID: "wordflow-level/player/campaign/1", Amount: 20, Reason: "wordflow/brush-hint",
	}))
	require.Error(t, validateSpendPoints(state, SpendPointsInput{
		LevelWorkflowID: "wordflow-level/player/campaign/1", Amount: 30, Reason: "wordflow/word-hint",
	}))
	require.Error(t, validateSpendPoints(state, SpendPointsInput{
		LevelWorkflowID: "wordflow-level/another/campaign/1", Amount: 10, Reason: "wordflow/letter-hint",
	}))
}

func TestStreakFreezeProtectsOneMissedDay(t *testing.T) {
	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	state := &PlayerState{
		CurrentStreak:     4,
		LastCompletionDay: dayKey(now.In(torontoLocation).AddDate(0, 0, -2)),
		StreakFreeze:      true,
	}

	expireStreakIfNeeded(state, now)
	require.Equal(t, 4, state.CurrentStreak)
	require.False(t, state.StreakFreeze)
	require.Equal(t, dayKey(now.In(torontoLocation).AddDate(0, 0, -1)), state.LastCompletionDay)
}

func TestBuyStreakFreezeDeductsPoints(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	state := &PlayerState{
		PlayerID: "player", Points: streakFreezePointCost,
	}
	var view game.PlayerView

	env.RegisterDelayedCallback(func() {
		env.SetContinueAsNewSuggested(true)
		env.UpdateWorkflow(UpdateBuyStreakFreeze, "buy-freeze", &testsuite.TestUpdateCallback{
			OnComplete: func(result any, err error) {
				require.NoError(t, err)
				view = result.(game.PlayerView)
			},
		}, BuyStreakFreezeInput{})
	}, time.Millisecond)
	env.ExecuteWorkflow(PlayerWorkflow, PlayerWorkflowInput{State: state})

	require.True(t, view.StreakFreeze)
	require.Zero(t, view.Points)
	var continueAsNew *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &continueAsNew)
}
