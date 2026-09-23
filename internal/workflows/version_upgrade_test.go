package workflows

import (
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func requestVersionUpgrade(env *testsuite.TestWorkflowEnvironment) {
	env.SetTargetWorkerDeploymentVersionChanged(true)
	env.SignalWorkflow(SignalRequestVersionUpgrade, struct{}{})
}

func requireVersionUpgrade(t *testing.T, env *testsuite.TestWorkflowEnvironment) *workflow.ContinueAsNewError {
	t.Helper()
	var continueAsNew *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &continueAsNew)
	require.EqualValues(t, workflow.ContinueAsNewVersioningBehaviorAutoUpgrade, continueAsNew.InitialVersioningBehavior)
	return continueAsNew
}

func TestCatalogUpgradeSignalContinuesAsNew(t *testing.T) {
	registration := testCampaignRegistration("campaign")
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { requestVersionUpgrade(env) }, time.Millisecond)

	env.ExecuteWorkflow(CatalogWorkflow, CatalogWorkflowInput{
		InitialCampaigns: []CampaignRegistration{registration},
	})

	var nextRun CatalogWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.Equal(t, []CampaignRegistration{registration}, nextRun.State.Campaigns)
}

func TestCatalogUpgradeDropsLegacyReferenceOnlyRegistrations(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { requestVersionUpgrade(env) }, time.Millisecond)

	env.ExecuteWorkflow(CatalogWorkflow, CatalogWorkflowInput{State: &CatalogState{
		Campaigns: []CampaignRegistration{{WorkflowID: "wordflow-campaign/legacy"}},
	}})

	var nextRun CatalogWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.Empty(t, nextRun.State.Campaigns)
}

func TestLeaderboardUpgradeSignalDrainsPendingScores(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	entry := game.LeaderboardEntry{PlayerID: "player", DisplayName: "Player", LifetimePointsEarned: 42, Version: 1}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalLeaderboardScore, entry)
		requestVersionUpgrade(env)
	}, time.Millisecond)

	env.ExecuteWorkflow(LeaderboardWorkflow, LeaderboardWorkflowInput{})

	var nextRun LeaderboardWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.Equal(t, []game.LeaderboardEntry{entry}, nextRun.State.Entries)
}

func TestPlayerUpgradeSignalContinuesWhenIdle(t *testing.T) {
	state := &PlayerState{PlayerID: "player", DisplayName: "Player", Points: 42}
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { requestVersionUpgrade(env) }, time.Millisecond)

	env.ExecuteWorkflow(PlayerWorkflow, PlayerWorkflowInput{State: state})

	var nextRun PlayerWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.Equal(t, state, nextRun.State)
}

func TestCampaignUpgradeSignalContinuesAsNew(t *testing.T) {
	state := &WordflowCampaignState{
		Definition: campaign.Definition{
			ID:   "campaign",
			Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		},
		Levels:     []game.Puzzle{{Level: 1, Title: "One", Letters: "ONE"}},
		Registered: true,
	}
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { requestVersionUpgrade(env) }, time.Millisecond)

	env.ExecuteWorkflow(WordflowCampaignWorkflow, WordflowCampaignWorkflowInput{State: state})

	var nextRun WordflowCampaignWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.False(t, nextRun.State.Registered)
	require.Equal(t, state.Definition, nextRun.State.Definition)
	require.Equal(t, state.Levels, nextRun.State.Levels)
}

func TestDailyChallengeUpgradeSignalInterruptsItsTimer(t *testing.T) {
	state := &DailyWordflowChallengeState{NextDate: "2026-09-24"}
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 9, 23, 12, 0, 0, 0, torontoLocation))
	env.RegisterDelayedCallback(func() { requestVersionUpgrade(env) }, time.Millisecond)

	env.ExecuteWorkflow(DailyWordflowChallengeWorkflow, DailyWordflowChallengeWorkflowInput{State: state})

	var nextRun DailyWordflowChallengeWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.Equal(t, state, nextRun.State)
}

func TestLevelUpgradeSignalContinuesAsNew(t *testing.T) {
	state := &WordflowLevelState{
		WorkflowID: "wordflow-level/player/campaign/1",
		PlayerID:   "player",
		CampaignID: "campaign",
		Puzzle: game.Puzzle{
			Level: 1, Title: "One", Letters: "ONE",
			Words: []game.PlacedWord{{Answer: "ONE"}},
		},
		StartedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { requestVersionUpgrade(env) }, time.Millisecond)

	env.ExecuteWorkflow(WordflowLevelWorkflow, WordflowLevelWorkflowInput{State: state})

	var nextRun WordflowLevelWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(requireVersionUpgrade(t, env).Input, &nextRun))
	require.Equal(t, state, nextRun.State)
}
