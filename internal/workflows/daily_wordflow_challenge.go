package workflows

import (
	"fmt"
	"time"

	"github.com/mjones/temporal-word-game/internal/game"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	dailyChallengeDateLayout  = "2006-01-02"
	dailyChallengeHistoryDays = 10
)

type DailyWordflowChallengeWorkflowInput struct {
	Date  string                       `json:"date,omitempty"`
	State *DailyWordflowChallengeState `json:"state,omitempty"`
}

type DailyWordflowChallengeState struct {
	NextDate      string                          `json:"nextDate"`
	RecentPuzzles []DailyWordflowChallengeArchive `json:"recentPuzzles"`
}

type DailyWordflowChallengeArchive struct {
	Date       string        `json:"date"`
	CampaignID string        `json:"campaignId"`
	Levels     []game.Puzzle `json:"levels"`
}

type GenerateDailyWordflowChallengeActivityInput struct {
	Date          string    `json:"date"`
	StartsAt      time.Time `json:"startsAt"`
	EndsAt        time.Time `json:"endsAt"`
	ExcludedWords []string  `json:"excludedWords,omitempty"`
}

func DailyWordflowChallengeWorkflow(ctx workflow.Context, input DailyWordflowChallengeWorkflowInput) error {
	state := input.State
	if state == nil {
		date, _, _, err := dailyChallengeWindow(workflow.Now(ctx), input.Date)
		if err != nil {
			return temporal.NewNonRetryableApplicationError(err.Error(), "invalid_daily_challenge_date", err)
		}
		state = &DailyWordflowChallengeState{NextDate: date}
	}

	if err := workflow.SetQueryHandler(ctx, QueryDailyWordflowChallengeState, func() (DailyWordflowChallengeState, error) {
		return cloneDailyWordflowChallengeState(state), nil
	}); err != nil {
		return err
	}

	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    90 * time.Second,
		ScheduleToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    5,
		},
	})

	for {
		date, startsAt, endsAt, err := nextDailyChallengeWindow(workflow.Now(ctx), state.NextDate)
		if err != nil {
			return temporal.NewNonRetryableApplicationError(err.Error(), "invalid_daily_challenge_date", err)
		}
		state.NextDate = date

		if wait := startsAt.Sub(workflow.Now(ctx)); wait > 0 {
			if err := workflow.Sleep(ctx, wait); err != nil {
				return err
			}
			if shouldContinueDailyWordflowChallenge(ctx) {
				return continueDailyWordflowChallengeAsNew(ctx, state)
			}
		}

		var campaignInput WordflowCampaignWorkflowInput
		if err := workflow.ExecuteActivity(activityCtx, ActivityGenerateDailyWordflowChallenge, GenerateDailyWordflowChallengeActivityInput{
			Date: date, StartsAt: startsAt, EndsAt: endsAt,
			ExcludedWords: recentDailyChallengeWords(state.RecentPuzzles),
		}).Get(ctx, &campaignInput); err != nil {
			return fmt.Errorf("generate daily challenge: %w", err)
		}

		expectedCampaignID := "daily-challenge-" + date
		if campaignInput.Definition.ID != expectedCampaignID {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("generated campaign ID %q does not match %q", campaignInput.Definition.ID, expectedCampaignID),
				"invalid_daily_challenge",
				nil,
			)
		}
		campaignWorkflowID := WordflowCampaignWorkflowID(campaignInput.Definition.ID)
		childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID:            campaignWorkflowID,
			WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
			ParentClosePolicy:     enums.PARENT_CLOSE_POLICY_ABANDON,
		})
		child := workflow.ExecuteChildWorkflow(childCtx, WordflowCampaignWorkflowName, campaignInput)
		if err := child.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
			return fmt.Errorf("start daily challenge campaign: %w", err)
		}

		archiveDailyChallenge(state, date, campaignInput)
		state.NextDate = endsAt.In(torontoLocation).Format(dailyChallengeDateLayout)
		if shouldContinueDailyWordflowChallenge(ctx) {
			return continueDailyWordflowChallengeAsNew(ctx, state)
		}

		if wait := endsAt.Sub(workflow.Now(ctx)); wait > 0 {
			if err := workflow.Sleep(ctx, wait); err != nil {
				return err
			}
		}
		if shouldContinueDailyWordflowChallenge(ctx) {
			return continueDailyWordflowChallengeAsNew(ctx, state)
		}
	}
}

func nextDailyChallengeWindow(now time.Time, requestedDate string) (string, time.Time, time.Time, error) {
	date, startsAt, endsAt, err := dailyChallengeWindow(now, requestedDate)
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	if !now.Before(endsAt) {
		return dailyChallengeWindow(now, "")
	}
	return date, startsAt, endsAt, nil
}

func dailyChallengeWindow(now time.Time, requestedDate string) (string, time.Time, time.Time, error) {
	date := requestedDate
	if date == "" {
		date = now.In(torontoLocation).Format(dailyChallengeDateLayout)
	}
	startsAt, err := time.ParseInLocation(dailyChallengeDateLayout, date, torontoLocation)
	if err != nil || startsAt.Format(dailyChallengeDateLayout) != date {
		return "", time.Time{}, time.Time{}, fmt.Errorf("date must use YYYY-MM-DD: %q", requestedDate)
	}
	return date, startsAt, startsAt.AddDate(0, 0, 1), nil
}

func archiveDailyChallenge(state *DailyWordflowChallengeState, date string, campaignInput WordflowCampaignWorkflowInput) {
	state.RecentPuzzles = append(state.RecentPuzzles, DailyWordflowChallengeArchive{
		Date:       date,
		CampaignID: campaignInput.Definition.ID,
		Levels:     append([]game.Puzzle(nil), campaignInput.Levels...),
	})
	if len(state.RecentPuzzles) > dailyChallengeHistoryDays {
		state.RecentPuzzles = append([]DailyWordflowChallengeArchive(nil), state.RecentPuzzles[len(state.RecentPuzzles)-dailyChallengeHistoryDays:]...)
	}
}

func recentDailyChallengeWords(archives []DailyWordflowChallengeArchive) []string {
	seen := map[string]bool{}
	var words []string
	for _, archive := range archives {
		for _, level := range archive.Levels {
			for _, word := range level.Words {
				if seen[word.Answer] {
					continue
				}
				seen[word.Answer] = true
				words = append(words, word.Answer)
			}
		}
	}
	return words
}

func cloneDailyWordflowChallengeState(state *DailyWordflowChallengeState) DailyWordflowChallengeState {
	clone := DailyWordflowChallengeState{NextDate: state.NextDate}
	for _, archive := range state.RecentPuzzles {
		copy := archive
		copy.Levels = append([]game.Puzzle(nil), archive.Levels...)
		clone.RecentPuzzles = append(clone.RecentPuzzles, copy)
	}
	return clone
}

func shouldContinueDailyWordflowChallenge(ctx workflow.Context) bool {
	return workflow.GetInfo(ctx).GetContinueAsNewSuggested() ||
		workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged()
}

func continueDailyWordflowChallengeAsNew(ctx workflow.Context, state *DailyWordflowChallengeState) error {
	options := workflow.ContinueAsNewErrorOptions{}
	if workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
		options.InitialVersioningBehavior = workflow.ContinueAsNewVersioningBehaviorAutoUpgrade
	}
	return workflow.NewContinueAsNewErrorWithOptions(ctx, options, DailyWordflowChallengeWorkflowName, DailyWordflowChallengeWorkflowInput{State: state})
}
