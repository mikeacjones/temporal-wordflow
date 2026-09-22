package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

type fakeResponseCreator struct {
	params    []responses.ResponseNewParams
	responses []*responses.Response
}

func (f *fakeResponseCreator) New(_ context.Context, params responses.ResponseNewParams, _ ...option.RequestOption) (*responses.Response, error) {
	f.params = append(f.params, params)
	return f.responses[len(f.params)-1], nil
}

func TestGenerateDailyWordflowChallengeRequiresTheTerminalToolCall(t *testing.T) {
	generated := validGeneratedDailyChallenge()
	arguments, err := json.Marshal(generated)
	require.NoError(t, err)
	responseJSON := fmt.Sprintf(`{"output":[{"type":"reasoning"},{"type":"function_call","name":%q,"call_id":"call-1","arguments":%q}]}`,
		dailyChallengeToolName, string(arguments))
	var response responses.Response
	require.NoError(t, json.Unmarshal([]byte(responseJSON), &response))

	fake := &fakeResponseCreator{responses: []*responses.Response{&response}}
	activity := &DailyChallenges{Responses: fake, Model: "test-model"}
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
	configured, err := activity.GenerateDailyWordflowChallenge(context.Background(), workflows.GenerateDailyWordflowChallengeActivityInput{
		Date: "2026-09-22", StartsAt: startsAt, EndsAt: startsAt.Add(24 * time.Hour),
		ExcludedWords: []string{"REPLAY"},
	})
	require.NoError(t, err)

	require.Len(t, fake.params, 1)
	params := fake.params[0]
	require.Equal(t, "test-model", params.Model)
	require.Equal(t, dailyChallengeToolName, params.ToolChoice.OfFunctionTool.Name)
	require.Len(t, params.Tools, 1)
	require.Equal(t, dailyChallengeToolName, params.Tools[0].OfFunction.Name)
	require.True(t, params.Tools[0].OfFunction.Strict.Value)
	require.Contains(t, params.Input.OfString.Value, "exactly three especially difficult")
	require.Contains(t, params.Input.OfString.Value, "hard 180-second limit")
	require.Contains(t, params.Input.OfString.Value, "15, 30, and 45 points")
	require.Contains(t, params.Input.OfString.Value, "REPLAY")

	require.Equal(t, "daily-challenge-2026-09-22", configured.Definition.ID)
	require.Equal(t, campaign.KindDailyChallenge, configured.Definition.Kind)
	require.True(t, configured.Definition.SingleAttempt)
	require.Equal(t, 3, configured.Definition.Unlock.InitialLevels)
	require.Equal(t, startsAt, *configured.Definition.StartsAt)
	require.Len(t, configured.Levels, 3)
	for _, level := range configured.Levels {
		require.Len(t, level.Words, 10)
		require.Equal(t, 180, level.TimeLimitSeconds)
		require.Equal(t, 20, level.BasePoints)
		require.Equal(t, game.HintPrices{Letter: 15, Brush: 30, Word: 45}, level.HintPrices)
	}
}

func TestExtractDailyChallengeToolCallRejectsProse(t *testing.T) {
	var response responses.Response
	require.NoError(t, json.Unmarshal([]byte(`{"output":[{"type":"message"}]}`), &response))

	_, _, err := extractDailyChallengeToolCall(&response)
	require.ErrorContains(t, err, "instead of ending with")
}

func TestGenerateDailyWordflowChallengeReturnsValidationErrorToTheTool(t *testing.T) {
	invalid := validGeneratedDailyChallenge()
	invalid.Levels[0].Words[9] = "ACTIVITY"
	invalidArguments, err := json.Marshal(invalid)
	require.NoError(t, err)
	validArguments, err := json.Marshal(validGeneratedDailyChallenge())
	require.NoError(t, err)

	response := func(id, callID string, arguments []byte) *responses.Response {
		responseJSON := fmt.Sprintf(`{"id":%q,"output":[{"type":"function_call","name":%q,"call_id":%q,"arguments":%q}]}`,
			id, dailyChallengeToolName, callID, string(arguments))
		var decoded responses.Response
		require.NoError(t, json.Unmarshal([]byte(responseJSON), &decoded))
		return &decoded
	}
	fake := &fakeResponseCreator{responses: []*responses.Response{
		response("response-1", "call-1", invalidArguments),
		response("response-2", "call-2", validArguments),
	}}
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	_, err = (&DailyChallenges{Responses: fake, Model: "test-model"}).GenerateDailyWordflowChallenge(
		context.Background(),
		workflows.GenerateDailyWordflowChallengeActivityInput{
			Date: "2026-09-22", StartsAt: startsAt, EndsAt: startsAt.Add(24 * time.Hour),
		},
	)
	require.NoError(t, err)
	require.Len(t, fake.params, 2)
	require.Len(t, fake.params[1].Input.OfInputItemList, 3)
	feedback := fake.params[1].Input.OfInputItemList[2].OfFunctionCallOutput
	require.Equal(t, "call-1", feedback.CallID.Value)
	require.Contains(t, feedback.Output.OfString.Value, "repeats answer")
}

func TestBuildDailyChallengeCampaignRejectsInvalidGeneratedWords(t *testing.T) {
	generated := validGeneratedDailyChallenge()
	generated.Levels[0].Words[9] = "ACTIVITY"
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildDailyChallengeCampaign(workflows.GenerateDailyWordflowChallengeActivityInput{
		Date: "2026-09-22", StartsAt: startsAt, EndsAt: startsAt.Add(24 * time.Hour),
	}, generated)
	require.ErrorContains(t, err, "repeats answer")
}

func TestBuildDailyChallengeCampaignRejectsARecentAnswer(t *testing.T) {
	generated := validGeneratedDailyChallenge()
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildDailyChallengeCampaign(workflows.GenerateDailyWordflowChallengeActivityInput{
		Date: "2026-09-22", StartsAt: startsAt, EndsAt: startsAt.Add(24 * time.Hour),
		ExcludedWords: []string{"activity"},
	}, generated)
	require.ErrorContains(t, err, `reuses answer "ACTIVITY"`)
}

func TestBuildDailyChallengeCampaignRejectsPastTensePadding(t *testing.T) {
	generated := validGeneratedDailyChallenge()
	generated.Levels[1].Words[9] = "CURE"
	startsAt := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	_, err := buildDailyChallengeCampaign(workflows.GenerateDailyWordflowChallengeActivityInput{
		Date: "2026-09-22", StartsAt: startsAt, EndsAt: startsAt.Add(24 * time.Hour),
	}, generated)
	require.ErrorContains(t, err, "past-tense form")
}

func validGeneratedDailyChallenge() generatedDailyChallenge {
	return generatedDailyChallenge{
		Description: "Three expert Wordflows with time pressure, richer rewards, premium hints, and only one attempt.",
		Levels: []generatedWordflowLevel{
			{
				Title: "Activity Pressure", Letters: "ACIITTVY",
				Words: []string{"ACTIVITY", "CITY", "TACT", "IVY", "ACT", "CAT", "VAT", "VIA", "TIC", "ICY"},
			},
			{
				Title: "Cloud Scramble", Letters: "CDEILORU",
				Words: []string{"CLOUDIER", "CLOUD", "COULD", "CRUDE", "CURED", "LURED", "IDLE", "RIDE", "RULE", "LOUD"},
			},
			{
				Title: "Rollback Rush", Letters: "ABCKLLOR",
				Words: []string{"ROLLBACK", "ROLL", "BACK", "BALL", "CALL", "ROCK", "LACK", "LOCK", "ORCA", "CAB"},
			},
		},
	}
}
