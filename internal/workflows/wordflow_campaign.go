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
	Registration CampaignRegistration `json:"registration"`
	UpdateID     string               `json:"updateId"`
}

type UnregisterCampaignActivityInput struct {
	CampaignID string `json:"campaignId"`
	WorkflowID string `json:"workflowId"`
	UpdateID   string `json:"updateId"`
}

type WordflowLevelQuery struct {
	CampaignID string                  `json:"campaignId"`
	Player     campaign.PlayerProgress `json:"player"`
	Level      int                     `json:"level"`
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
	Query WordflowLevelQuery `json:"query"`
}

func WordflowCampaignWorkflow(ctx workflow.Context, input WordflowCampaignWorkflowInput) error {
	state := input.State
	if state == nil {
		state = &WordflowCampaignState{Definition: input.Definition, Levels: input.Levels}
	}
	if err := validateWordflowCampaignLevels(state.Levels); err != nil {
		return err
	}
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	registration := func() CampaignRegistration {
		return CampaignRegistration{
			WorkflowID: workflowID,
			Definition: state.Definition,
			Levels:     state.Levels,
		}
	}

	if err := workflow.SetQueryHandler(ctx, QueryCampaignSummary, func() (campaign.Summary, error) {
		// Query results are not recorded in Workflow history, so they can reflect
		// wall time as long as the value never mutates durable Workflow state.
		now := time.Now().UTC() //workflowcheck:ignore
		return wordflowCampaignSummary(registration(), now), nil
	}); err != nil {
		return err
	}
	if err := workflow.SetQueryHandler(ctx, QueryCampaignView, func(input campaign.QueryInput) (campaign.View, error) {
		now := time.Now().UTC() //workflowcheck:ignore
		return wordflowCampaignView(registration(), input.Player, now), nil
	}); err != nil {
		return err
	}
	if err := workflow.SetQueryHandler(ctx, QueryWordflowLevel, func(input WordflowLevelQuery) (WordflowLevelResolution, error) {
		now := time.Now().UTC() //workflowcheck:ignore
		return resolveWordflowLevel(registration(), input, now), nil
	}); err != nil {
		return err
	}

	if !state.Registered {
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
		if err := workflow.ExecuteActivity(activityCtx, ActivityRegisterCampaign, RegisterCampaignActivityInput{
			Registration: registration(),
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
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
		if err := workflow.ExecuteActivity(activityCtx, ActivityUnregisterCampaign, UnregisterCampaignActivityInput{
			CampaignID: state.Definition.ID,
			WorkflowID: workflowID,
			UpdateID:   fmt.Sprintf("unregister/%s/%s", state.Definition.ID, workflow.GetInfo(ctx).WorkflowExecution.RunID),
		}).Get(ctx, nil); err != nil {
			return fmt.Errorf("unregister campaign: %w", err)
		}
		return nil
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

func wordflowCampaignSummary(registration CampaignRegistration, now time.Time) campaign.Summary {
	definition := registration.Definition
	return campaign.Summary{
		CampaignID:  definition.ID,
		Kind:        definition.Kind,
		WorkflowID:  registration.WorkflowID,
		Game:        definition.Game,
		Title:       definition.Title,
		Description: definition.Description,
		Status:      wordflowCampaignStatus(definition, now),
		StartsAt:    definition.StartsAt,
		EndsAt:      definition.EndsAt,
		TotalLevels: len(registration.Levels),
	}
}

func wordflowCampaignView(registration CampaignRegistration, player campaign.PlayerProgress, now time.Time) campaign.View {
	definition := registration.Definition
	summary := wordflowCampaignSummary(registration, now)
	progress := findPlayerCampaign(player.Campaigns, definition.ID)
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

	eligible, reason := campaignEligibility(definition, player, summary.Status)
	if player.ActiveCampaignID != "" && player.ActiveCampaignID != definition.ID {
		eligible = false
		reason = "Finish the active level before starting another campaign."
	}
	unlocked := unlockedWordflowLevels(definition.Unlock, joinedAt, now, len(registration.Levels))
	levels := make([]campaign.LevelView, 0, len(registration.Levels))
	for _, puzzle := range registration.Levels {
		status := campaign.LevelLocked
		switch {
		case puzzle.Level < nextLevel:
			status = campaign.LevelComplete
		case player.ActiveCampaignID == definition.ID && player.ActiveLevel == puzzle.Level:
			status = campaign.LevelActive
		case eligible && puzzle.Level == nextLevel && puzzle.Level <= unlocked:
			status = campaign.LevelAvailable
		case eligible && puzzle.Level <= unlocked:
			status = campaign.LevelUnlocked
		}
		levels = append(levels, campaign.LevelView{
			Level: puzzle.Level, Title: puzzle.Title, Status: status,
			UnlockedAt: wordflowLevelUnlockTime(definition.Unlock, joinedAt, puzzle.Level),
		})
	}

	return campaign.View{
		CampaignID: definition.ID, Kind: definition.Kind, WorkflowID: summary.WorkflowID,
		Game: definition.Game, Title: definition.Title, Description: definition.Description,
		Status: summary.Status, StartsAt: definition.StartsAt, EndsAt: definition.EndsAt,
		Eligible: eligible, Failed: failed, LockedReason: reason, NextLevel: nextLevel,
		CompletedLevels: completedLevels, TotalLevels: len(registration.Levels), Levels: levels,
	}
}

func resolveWordflowLevel(registration CampaignRegistration, input WordflowLevelQuery, now time.Time) WordflowLevelResolution {
	view := wordflowCampaignView(registration, input.Player, now)
	resolution := WordflowLevelResolution{
		CampaignID:  registration.Definition.ID,
		TotalLevels: len(registration.Levels),
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
	resolution.Puzzle = registration.Levels[input.Level-1]
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
