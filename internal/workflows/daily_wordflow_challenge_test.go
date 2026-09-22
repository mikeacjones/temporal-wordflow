package workflows

import (
	"context"
	"fmt"
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

func testGenerateDailyWordflowChallenge(context.Context, GenerateDailyWordflowChallengeActivityInput) (WordflowCampaignWorkflowInput, error) {
	return WordflowCampaignWorkflowInput{}, nil
}

func TestDailyWordflowChallengeRetainsHistoryAndContinuesOnlyWhenSuggested(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation))
	env.SetContinueAsNewSuggested(true)
	env.RegisterActivityWithOptions(testGenerateDailyWordflowChallenge, activity.RegisterOptions{Name: ActivityGenerateDailyWordflowChallenge})

	state := &DailyWordflowChallengeState{NextDate: "2026-09-22"}
	for index := range dailyChallengeHistoryDays {
		state.RecentPuzzles = append(state.RecentPuzzles, DailyWordflowChallengeArchive{
			Date: fmt.Sprintf("2026-09-%02d", 12+index), CampaignID: fmt.Sprintf("old-%d", index),
			Levels: []game.Puzzle{{Words: []game.PlacedWord{{Answer: fmt.Sprintf("OLDWORD%d", index)}}}},
		})
	}
	campaignInput := WordflowCampaignWorkflowInput{
		Definition: campaign.Definition{
			ID:   "daily-challenge-2026-09-22",
			Game: campaign.GameSummary{ID: campaign.GameWordflow, Title: "Wordflow"},
		},
		Levels: []game.Puzzle{{Level: 1, Words: []game.PlacedWord{{Answer: "NEWWORD"}}}},
	}
	var generatedInput GenerateDailyWordflowChallengeActivityInput
	env.OnActivity(ActivityGenerateDailyWordflowChallenge, mock.Anything, mock.Anything).
		Run(func(arguments mock.Arguments) {
			generatedInput = arguments.Get(1).(GenerateDailyWordflowChallengeActivityInput)
		}).Return(campaignInput, nil).Once()

	var startedCampaign WordflowCampaignWorkflowInput
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, input WordflowCampaignWorkflowInput) error {
		startedCampaign = input
		return nil
	}, workflow.RegisterOptions{Name: WordflowCampaignWorkflowName})

	env.ExecuteWorkflow(DailyWordflowChallengeWorkflow, DailyWordflowChallengeWorkflowInput{State: state})

	var continueAsNew *workflow.ContinueAsNewError
	require.ErrorAs(t, env.GetWorkflowError(), &continueAsNew)
	var nextRun DailyWordflowChallengeWorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(continueAsNew.Input, &nextRun))
	require.Equal(t, "2026-09-23", nextRun.State.NextDate)
	require.Len(t, nextRun.State.RecentPuzzles, dailyChallengeHistoryDays)
	require.Equal(t, "2026-09-13", nextRun.State.RecentPuzzles[0].Date)
	require.Equal(t, "2026-09-22", nextRun.State.RecentPuzzles[9].Date)
	require.Equal(t, "daily-challenge-2026-09-22", nextRun.State.RecentPuzzles[9].CampaignID)
	require.Len(t, generatedInput.ExcludedWords, dailyChallengeHistoryDays)
	require.Equal(t, "OLDWORD0", generatedInput.ExcludedWords[0])
	require.Equal(t, campaignInput, startedCampaign)
	env.AssertExpectations(t)
}

func TestRecentDailyChallengeWordsPreservesOrderAndDeduplicates(t *testing.T) {
	archives := []DailyWordflowChallengeArchive{
		{Levels: []game.Puzzle{{Words: []game.PlacedWord{{Answer: "FIRST"}, {Answer: "SHARED"}}}}},
		{Levels: []game.Puzzle{{Words: []game.PlacedWord{{Answer: "SHARED"}, {Answer: "LAST"}}}}},
	}
	require.Equal(t, []string{"FIRST", "SHARED", "LAST"}, recentDailyChallengeWords(archives))
}

func TestDailyChallengeWindowDefaultsToTheCurrentTorontoDate(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	date, startsAt, endsAt, err := dailyChallengeWindow(now, "")
	require.NoError(t, err)
	require.Equal(t, "2026-09-21", date)
	require.Equal(t, time.Date(2026, 9, 21, 0, 0, 0, 0, torontoLocation), startsAt)
	require.Equal(t, startsAt.AddDate(0, 0, 1), endsAt)
}

func TestNextDailyChallengeWindowSkipsExpiredDates(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, torontoLocation)
	date, startsAt, _, err := nextDailyChallengeWindow(now, "2026-09-19")
	require.NoError(t, err)
	require.Equal(t, "2026-09-22", date)
	require.Equal(t, time.Date(2026, 9, 22, 0, 0, 0, 0, torontoLocation), startsAt)
}

func TestDailyChallengeWindowRejectsInvalidDate(t *testing.T) {
	_, _, _, err := dailyChallengeWindow(time.Now(), "September 22")
	require.ErrorContains(t, err, "YYYY-MM-DD")
}
