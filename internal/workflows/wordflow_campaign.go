package workflows

import (
	"fmt"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type WordflowCampaignWorkflowInput struct {
	Definition campaign.Definition    `json:"definition,omitempty"`
	Levels     []game.Puzzle          `json:"levels,omitempty"`
	State      *WordflowCampaignState `json:"state,omitempty"`
}

type WordflowCampaignState struct {
	Definition campaign.Definition `json:"definition"`
	Levels     []game.Puzzle       `json:"levels"`
	Registered bool                `json:"registered"`
}

type RegisterCampaignActivityInput struct {
	Registration campaign.Registration `json:"registration"`
	UpdateID     string                `json:"updateId"`
}

type WordflowLevelQuery struct {
	Player campaign.PlayerProgress `json:"player"`
	Level  int                     `json:"level"`
}

type WordflowLevelResolution struct {
	Allowed     bool        `json:"allowed"`
	Reason      string      `json:"reason,omitempty"`
	ErrorType   string      `json:"errorType,omitempty"`
	CampaignID  string      `json:"campaignId"`
	TotalLevels int         `json:"totalLevels"`
	Puzzle      game.Puzzle `json:"puzzle"`
}

type ResolveWordflowLevelActivityInput struct {
	CampaignWorkflowID string             `json:"campaignWorkflowId"`
	Query              WordflowLevelQuery `json:"query"`
}

func WordflowCampaignWorkflow(ctx workflow.Context, input WordflowCampaignWorkflowInput) error {
	state := input.State
	if state == nil {
		state = &WordflowCampaignState{Definition: input.Definition, Levels: input.Levels}
	}
	if err := validateWordflowCampaignLevels(state.Levels); err != nil {
		return err
	}

	if err := workflow.SetQueryHandler(ctx, QueryCampaignSummary, func() (campaign.Summary, error) {
		return wordflowCampaignSummary(ctx, state), nil
	}); err != nil {
		return err
	}
	if err := workflow.SetQueryHandler(ctx, QueryCampaignView, func(input campaign.QueryInput) (campaign.View, error) {
		return wordflowCampaignView(ctx, state, input.Player), nil
	}); err != nil {
		return err
	}
	if err := workflow.SetQueryHandler(ctx, QueryWordflowLevel, func(input WordflowLevelQuery) (WordflowLevelResolution, error) {
		return resolveWordflowLevel(ctx, state, input), nil
	}); err != nil {
		return err
	}

	if !state.Registered {
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
		registration := campaign.Registration{
			CampaignID: state.Definition.ID,
			WorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID,
			Game:       state.Definition.Game,
		}
		if err := workflow.ExecuteActivity(activityCtx, ActivityRegisterCampaign, RegisterCampaignActivityInput{
			Registration: registration,
			UpdateID:     fmt.Sprintf("register/%s/%s", state.Definition.ID, workflow.GetInfo(ctx).WorkflowExecution.RunID),
		}).Get(ctx, nil); err != nil {
			return fmt.Errorf("register campaign: %w", err)
		}
		state.Registered = true
	}

	if state.Definition.EndsAt != nil {
		remaining := state.Definition.EndsAt.Sub(workflow.Now(ctx))
		if remaining > 0 {
			if err := workflow.Sleep(ctx, remaining); err != nil {
				return err
			}
		}
	}
	return workflow.Await(ctx, func() bool { return false })
}

func validateWordflowCampaignLevels(levels []game.Puzzle) error {
	for _, puzzle := range levels {
		if len([]rune(puzzle.Letters)) > game.MaxWordflowLetters {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("level %d has more than %d letters", puzzle.Level, game.MaxWordflowLetters),
				"invalid_campaign",
				nil,
			)
		}
		if puzzle.TimeLimitSeconds < 0 || puzzle.BasePoints < 0 ||
			puzzle.HintPrices.Letter < 0 || puzzle.HintPrices.Brush < 0 || puzzle.HintPrices.Word < 0 {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("level %d has invalid gameplay settings", puzzle.Level),
				"invalid_campaign",
				nil,
			)
		}
	}
	return nil
}

func wordflowCampaignSummary(ctx workflow.Context, state *WordflowCampaignState) campaign.Summary {
	return campaign.Summary{
		CampaignID:  state.Definition.ID,
		Kind:        state.Definition.Kind,
		WorkflowID:  workflow.GetInfo(ctx).WorkflowExecution.ID,
		Game:        state.Definition.Game,
		Title:       state.Definition.Title,
		Description: state.Definition.Description,
		Status:      wordflowCampaignStatus(state.Definition, workflow.Now(ctx)),
		StartsAt:    state.Definition.StartsAt,
		EndsAt:      state.Definition.EndsAt,
		TotalLevels: len(state.Levels),
	}
}

func wordflowCampaignView(ctx workflow.Context, state *WordflowCampaignState, player campaign.PlayerProgress) campaign.View {
	now := workflow.Now(ctx)
	summary := wordflowCampaignSummary(ctx, state)
	progress := findPlayerCampaign(player.Campaigns, state.Definition.ID)
	joinedAt := now
	nextLevel := 1
	completedLevels := 0
	failed := false
	if progress != nil {
		joinedAt = progress.JoinedAt
		nextLevel = progress.NextLevel
		completedLevels = progress.CompletedLevels
		failed = progress.Failed
	}

	eligible, reason := campaignEligibility(state.Definition, player, summary.Status)
	if player.ActiveCampaignID != "" && player.ActiveCampaignID != state.Definition.ID {
		eligible = false
		reason = "Finish the active level before starting another campaign."
	}
	unlocked := unlockedWordflowLevels(state.Definition.Unlock, joinedAt, now, len(state.Levels))
	levels := make([]campaign.LevelView, 0, len(state.Levels))
	for _, puzzle := range state.Levels {
		status := campaign.LevelLocked
		switch {
		case puzzle.Level < nextLevel:
			status = campaign.LevelComplete
		case player.ActiveCampaignID == state.Definition.ID && player.ActiveLevel == puzzle.Level:
			status = campaign.LevelActive
		case eligible && puzzle.Level == nextLevel && puzzle.Level <= unlocked:
			status = campaign.LevelAvailable
		case eligible && puzzle.Level <= unlocked:
			status = campaign.LevelUnlocked
		}
		levels = append(levels, campaign.LevelView{
			Level: puzzle.Level, Title: puzzle.Title, Status: status,
			UnlockedAt: wordflowLevelUnlockTime(state.Definition.Unlock, joinedAt, puzzle.Level),
		})
	}

	return campaign.View{
		CampaignID: state.Definition.ID, Kind: state.Definition.Kind, WorkflowID: summary.WorkflowID,
		Game: state.Definition.Game, Title: state.Definition.Title, Description: state.Definition.Description,
		Status: summary.Status, StartsAt: state.Definition.StartsAt, EndsAt: state.Definition.EndsAt,
		Eligible: eligible, Failed: failed, LockedReason: reason, NextLevel: nextLevel,
		CompletedLevels: completedLevels, TotalLevels: len(state.Levels), Levels: levels,
	}
}

func resolveWordflowLevel(ctx workflow.Context, state *WordflowCampaignState, input WordflowLevelQuery) WordflowLevelResolution {
	view := wordflowCampaignView(ctx, state, input.Player)
	resolution := WordflowLevelResolution{
		CampaignID:  state.Definition.ID,
		TotalLevels: len(state.Levels),
	}
	if !view.Eligible {
		resolution.Reason = view.LockedReason
		resolution.ErrorType = "campaign_locked"
		return resolution
	}
	if input.Level != view.NextLevel {
		resolution.Reason = fmt.Sprintf("level %d is not next; expected level %d", input.Level, view.NextLevel)
		resolution.ErrorType = "level_not_next"
		return resolution
	}
	if input.Level < 1 || input.Level > len(view.Levels) || view.Levels[input.Level-1].Status != campaign.LevelAvailable {
		resolution.Reason = "the next level has not unlocked yet"
		resolution.ErrorType = "level_locked"
		return resolution
	}
	resolution.Allowed = true
	resolution.Puzzle = state.Levels[input.Level-1]
	return resolution
}

func wordflowCampaignStatus(definition campaign.Definition, now time.Time) campaign.Status {
	if definition.StartsAt != nil && now.Before(*definition.StartsAt) {
		return campaign.StatusUpcoming
	}
	if definition.EndsAt != nil && !now.Before(*definition.EndsAt) {
		return campaign.StatusEnded
	}
	return campaign.StatusActive
}

func campaignEligibility(definition campaign.Definition, player campaign.PlayerProgress, status campaign.Status) (bool, string) {
	switch status {
	case campaign.StatusUpcoming:
		return false, "This campaign has not started yet."
	case campaign.StatusEnded:
		return false, "This campaign has ended."
	}
	if progress := findPlayerCampaign(player.Campaigns, definition.ID); definition.SingleAttempt && progress != nil && progress.Failed {
		return false, "Today's daily challenge attempt is over."
	}
	for _, requiredCampaign := range definition.Requirements.Campaigns {
		progress := findPlayerCampaign(player.Campaigns, requiredCampaign)
		if progress == nil || !progress.Completed {
			return false, "Complete campaign " + requiredCampaign + " first."
		}
	}
	for _, requiredLevel := range definition.Requirements.Levels {
		progress := findPlayerCampaign(player.Campaigns, requiredLevel.CampaignID)
		if progress == nil || progress.CompletedLevels < requiredLevel.Level {
			return false, fmt.Sprintf("Complete level %d of %s first.", requiredLevel.Level, requiredLevel.CampaignID)
		}
	}
	return true, ""
}

func findPlayerCampaign(progress []campaign.PlayerCampaignProgress, campaignID string) *campaign.PlayerCampaignProgress {
	for index := range progress {
		if progress[index].CampaignID == campaignID {
			return &progress[index]
		}
	}
	return nil
}

func unlockedWordflowLevels(policy campaign.UnlockPolicy, joinedAt, now time.Time, total int) int {
	unlocked := policy.InitialLevels
	if policy.LevelsPerInterval > 0 && policy.Interval > 0 && now.After(joinedAt) {
		unlocked += int(now.Sub(joinedAt)/policy.Interval) * policy.LevelsPerInterval
	}
	return min(max(unlocked, 0), total)
}

func wordflowLevelUnlockTime(policy campaign.UnlockPolicy, joinedAt time.Time, level int) *time.Time {
	if level <= policy.InitialLevels || policy.LevelsPerInterval <= 0 || policy.Interval <= 0 {
		value := joinedAt
		return &value
	}
	intervals := (level - policy.InitialLevels + policy.LevelsPerInterval - 1) / policy.LevelsPerInterval
	value := joinedAt.Add(time.Duration(intervals) * policy.Interval)
	return &value
}
