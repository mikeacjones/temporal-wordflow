package workflows

import (
	"fmt"
	"time"
	_ "time/tzdata"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	playerTimeZone        = "America/Toronto"
	streakFreezePointCost = 50
)

var torontoLocation = mustLoadLocation(playerTimeZone)

type PlayerWorkflowInput struct {
	PlayerID string       `json:"playerId,omitempty"`
	State    *PlayerState `json:"state,omitempty"`
}

// PlayerState is the complete Continue-As-New checkpoint for one player.
type PlayerState struct {
	PlayerID             string                            `json:"playerId"`
	DisplayName          string                            `json:"displayName"`
	PasswordHash         string                            `json:"passwordHash"`
	CreatedAt            time.Time                         `json:"createdAt"`
	LastSeenAt           time.Time                         `json:"lastSeenAt"`
	LastCompletionDay    string                            `json:"lastCompletionDay"`
	CurrentStreak        int                               `json:"currentStreak"`
	BestStreak           int                               `json:"bestStreak"`
	Points               int                               `json:"points"`
	LifetimePointsEarned int                               `json:"lifetimePointsEarned"`
	StreakFreeze         bool                              `json:"streakFreeze"`
	Campaigns            []campaign.PlayerCampaignProgress `json:"campaigns"`
	CompletedLevels      []game.LevelCompletion            `json:"completedLevels"`
	Rewards              []game.Reward                     `json:"rewards"`
	ActiveGame           *game.ActiveGame                  `json:"activeGame,omitempty"`
}

type AuthenticatePlayerInput struct {
	DisplayName  string `json:"displayName"`
	PasswordHash string `json:"passwordHash"`
	Register     bool   `json:"register"`
}

type StartLevelInput struct {
	CampaignID string `json:"campaignId"`
	Level      int    `json:"level"`
}

type SpendPointsInput struct {
	LevelWorkflowID string `json:"levelWorkflowId"`
	Amount          int    `json:"amount"`
	Reason          string `json:"reason"`
}

type SpendPointsResult struct {
	Spent     int `json:"spent"`
	Remaining int `json:"remaining"`
}

type SpendPointsActivityInput struct {
	PlayerWorkflowID string           `json:"playerWorkflowId"`
	UpdateID         string           `json:"updateId"`
	Spend            SpendPointsInput `json:"spend"`
}

type BuyStreakFreezeInput struct{}

func PlayerWorkflow(ctx workflow.Context, input PlayerWorkflowInput) error {
	state := initialPlayerState(ctx, input)
	lock := workflow.NewMutex(ctx)
	changed := workflow.NewBufferedChannel(ctx, 1)
	var levelFuture workflow.ChildWorkflowFuture
	var leaderboardFuture workflow.Future

	if err := workflow.SetQueryHandler(ctx, QueryPlayerState, func() (game.PlayerView, error) {
		return playerView(state), nil
	}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateAuthenticatePlayer,
		func(ctx workflow.Context, update AuthenticatePlayerInput) (game.PlayerView, error) {
			if err := lock.Lock(ctx); err != nil {
				return game.PlayerView{}, err
			}
			defer lock.Unlock()

			now := workflow.Now(ctx)
			if err := validateCredentials(state, update); err != nil {
				return game.PlayerView{}, err
			}
			if state.PasswordHash == "" {
				// The first accepted registration Update claims this username.
				state.PasswordHash = update.PasswordHash
				state.DisplayName = update.DisplayName
			}
			state.LastSeenAt = now
			expireStreakIfNeeded(state, now)
			changed.SendAsync(true)
			return playerView(state), nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, update AuthenticatePlayerInput) error {
				return validateCredentials(state, update)
			},
		}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateStartLevel, func(ctx workflow.Context, update StartLevelInput) (game.PlayerView, error) {
		if err := lock.Lock(ctx); err != nil {
			return game.PlayerView{}, err
		}
		defer lock.Unlock()

		now := workflow.Now(ctx)
		if state.ActiveGame != nil {
			return game.PlayerView{}, temporal.NewApplicationError("finish the active level before starting another", "game_active")
		}

		playerProgress := campaignProgressForStart(state, update.CampaignID, now)
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
		var resolution WordflowLevelResolution
		if err := workflow.ExecuteActivity(activityCtx, ActivityResolveWordflowLevel, ResolveWordflowLevelActivityInput{
			CampaignWorkflowID: WordflowCampaignWorkflowID(update.CampaignID),
			Query:              WordflowLevelQuery{Player: playerProgress, Level: update.Level},
		}).Get(ctx, &resolution); err != nil {
			return game.PlayerView{}, fmt.Errorf("resolve campaign level: %w", err)
		}
		if !resolution.Allowed {
			return game.PlayerView{}, temporal.NewApplicationError(resolution.Reason, resolution.ErrorType)
		}

		levelID := WordflowLevelWorkflowID(state.PlayerID, update.CampaignID, update.Level)
		childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID: levelID,
		})
		future := workflow.ExecuteChildWorkflow(childCtx, WordflowLevelWorkflowName, WordflowLevelWorkflowInput{
			PlayerID: state.PlayerID, CampaignID: update.CampaignID, Puzzle: resolution.Puzzle,
		})
		// Wait only for Temporal to record the child start. Its completion stays
		// in the selector below.
		if err := future.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
			return game.PlayerView{}, fmt.Errorf("start level workflow: %w", err)
		}
		levelFuture = future

		joinPlayerCampaign(state, update.CampaignID, now, resolution.TotalLevels)
		state.ActiveGame = &game.ActiveGame{
			WorkflowID: levelID, CampaignID: update.CampaignID,
			Level: update.Level, Title: resolution.Puzzle.Title, TotalLevels: resolution.TotalLevels,
		}
		state.LastSeenAt = now
		changed.SendAsync(true)
		return playerView(state), nil
	}, workflow.UpdateHandlerOptions{
		Validator: func(_ workflow.Context, update StartLevelInput) error {
			if update.CampaignID == "" || update.Level < 1 {
				return temporal.NewApplicationError("campaign ID and level are required", "invalid_level")
			}
			if state.ActiveGame != nil {
				return temporal.NewApplicationError("finish the active level before starting another", "game_active")
			}
			return nil
		},
	}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateSpendPoints,
		func(ctx workflow.Context, update SpendPointsInput) (SpendPointsResult, error) {
			if err := lock.Lock(ctx); err != nil {
				return SpendPointsResult{}, err
			}
			defer lock.Unlock()

			if err := validateSpendPoints(state, update); err != nil {
				return SpendPointsResult{}, err
			}
			state.Points -= update.Amount
			state.LastSeenAt = workflow.Now(ctx)
			changed.SendAsync(true)
			return SpendPointsResult{Spent: update.Amount, Remaining: state.Points}, nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, update SpendPointsInput) error {
				return validateSpendPoints(state, update)
			},
		}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateBuyStreakFreeze,
		func(ctx workflow.Context, _ BuyStreakFreezeInput) (game.PlayerView, error) {
			if err := lock.Lock(ctx); err != nil {
				return game.PlayerView{}, err
			}
			defer lock.Unlock()

			if err := validateStreakFreezePurchase(state); err != nil {
				return game.PlayerView{}, err
			}
			state.Points -= streakFreezePointCost
			state.StreakFreeze = true
			state.LastSeenAt = workflow.Now(ctx)
			changed.SendAsync(true)
			return playerView(state), nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(_ workflow.Context, _ BuyStreakFreezeInput) error {
				return validateStreakFreezePurchase(state)
			},
		}); err != nil {
		return err
	}

	for {
		// Keep the player responsive while its level runs. Update handlers notify
		// changed; the child result is read only after its future is ready.
		selector := workflow.NewSelector(ctx)
		var levelErr error
		if levelFuture != nil {
			selector.AddFuture(levelFuture, func(future workflow.Future) {
				var result campaign.LevelResult
				if err := future.Get(ctx, &result); err != nil {
					levelErr = fmt.Errorf("level workflow failed: %w", err)
					return
				}
				if applyLevelResult(state, result) {
					activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
					leaderboardFuture = workflow.ExecuteActivity(activityCtx, ActivityPublishLeaderboard, game.LeaderboardEntry{
						PlayerID: state.PlayerID, DisplayName: state.DisplayName,
						LifetimePointsEarned: state.LifetimePointsEarned,
						CompletedLevels:      len(state.CompletedLevels), Version: len(state.CompletedLevels),
					})
				}
				levelFuture = nil
			})
		}
		if leaderboardFuture != nil {
			selector.AddFuture(leaderboardFuture, func(future workflow.Future) {
				if err := future.Get(ctx, nil); err != nil {
					levelErr = fmt.Errorf("publish leaderboard: %w", err)
					return
				}
				leaderboardFuture = nil
			})
		}
		selector.AddReceive(changed, func(channel workflow.ReceiveChannel, _ bool) {
			var ignored bool
			channel.Receive(ctx, &ignored)
		})
		selector.Select(ctx)
		if levelErr != nil {
			return levelErr
		}

		if leaderboardFuture == nil && shouldContinuePlayer(ctx, state) {
			if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
				return err
			}
			return continuePlayerAsNew(ctx, state)
		}
	}
}

func initialPlayerState(ctx workflow.Context, input PlayerWorkflowInput) *PlayerState {
	if input.State != nil {
		return input.State
	}

	now := workflow.Now(ctx)
	return &PlayerState{PlayerID: input.PlayerID, CreatedAt: now, LastSeenAt: now}
}

func playerView(state *PlayerState) game.PlayerView {
	return game.PlayerView{
		PlayerID:             state.PlayerID,
		DisplayName:          state.DisplayName,
		CreatedAt:            state.CreatedAt,
		LastSeenAt:           state.LastSeenAt,
		CurrentStreak:        state.CurrentStreak,
		BestStreak:           state.BestStreak,
		Points:               state.Points,
		LifetimePointsEarned: state.LifetimePointsEarned,
		StreakFreeze:         state.StreakFreeze,
		StreakFreezeCost:     streakFreezePointCost,
		Campaigns:            append([]campaign.PlayerCampaignProgress(nil), state.Campaigns...),
		CompletedLevelCount:  len(state.CompletedLevels),
		CompletedLevels:      append([]game.LevelCompletion(nil), state.CompletedLevels...),
		Rewards:              append([]game.Reward(nil), state.Rewards...),
		ActiveGame:           cloneActiveGame(state.ActiveGame),
	}
}

func applyLevelResult(state *PlayerState, result campaign.LevelResult) bool {
	if state.ActiveGame == nil || state.ActiveGame.CampaignID != result.CampaignID || state.ActiveGame.Level != result.Level {
		return false
	}
	progress := findPlayerCampaign(state.Campaigns, result.CampaignID)
	if progress == nil || progress.NextLevel != result.Level {
		return false
	}

	state.ActiveGame = nil
	updateStreak(state, result.CompletedAt)
	points := 0
	for _, award := range result.Awards {
		points += award.Points
		state.Rewards = append(state.Rewards, game.Reward{
			CampaignID: result.CampaignID, Level: result.Level,
			Description: award.Description, Points: award.Points, AwardedAt: result.CompletedAt,
		})
	}
	state.CompletedLevels = append(state.CompletedLevels, game.LevelCompletion{
		CampaignID: result.CampaignID, Level: result.Level,
		Attempts: result.Attempts, CompletedAt: result.CompletedAt, Points: points,
	})
	progress.NextLevel++
	progress.CompletedLevels++
	progress.Completed = progress.CompletedLevels >= progress.TotalLevels
	state.LastSeenAt = result.CompletedAt
	state.Points += points
	state.LifetimePointsEarned += points
	return true
}

func updateStreak(state *PlayerState, completedAt time.Time) {
	today := dayKey(completedAt)
	if state.LastCompletionDay == today {
		return
	}

	yesterday := dayKey(completedAt.In(torontoLocation).AddDate(0, 0, -1))
	if state.LastCompletionDay == yesterday {
		state.CurrentStreak++
	} else {
		state.CurrentStreak = 1
	}
	state.BestStreak = max(state.BestStreak, state.CurrentStreak)
	state.LastCompletionDay = today
}

func expireStreakIfNeeded(state *PlayerState, now time.Time) {
	if state.LastCompletionDay == "" {
		return
	}
	today := dayKey(now)
	yesterday := dayKey(now.In(torontoLocation).AddDate(0, 0, -1))
	if state.LastCompletionDay == today || state.LastCompletionDay == yesterday {
		return
	}

	dayBeforeYesterday := dayKey(now.In(torontoLocation).AddDate(0, 0, -2))
	if state.StreakFreeze && state.LastCompletionDay == dayBeforeYesterday {
		state.StreakFreeze = false
		state.LastCompletionDay = yesterday
		return
	}
	state.CurrentStreak = 0
}

func dayKey(value time.Time) string {
	return value.In(torontoLocation).Format("2006-01-02")
}

func campaignProgressForStart(state *PlayerState, campaignID string, joinedAt time.Time) campaign.PlayerProgress {
	progress := campaign.PlayerProgress{
		Campaigns: append([]campaign.PlayerCampaignProgress(nil), state.Campaigns...),
	}
	if state.ActiveGame != nil {
		progress.ActiveCampaignID = state.ActiveGame.CampaignID
		progress.ActiveLevel = state.ActiveGame.Level
	}
	if findPlayerCampaign(progress.Campaigns, campaignID) == nil {
		progress.Campaigns = append(progress.Campaigns, campaign.PlayerCampaignProgress{
			CampaignID: campaignID, JoinedAt: joinedAt, NextLevel: 1,
		})
	}
	return progress
}

func joinPlayerCampaign(state *PlayerState, campaignID string, joinedAt time.Time, totalLevels int) {
	if progress := findPlayerCampaign(state.Campaigns, campaignID); progress != nil {
		progress.TotalLevels = totalLevels
		return
	}
	state.Campaigns = append(state.Campaigns, campaign.PlayerCampaignProgress{
		CampaignID: campaignID, JoinedAt: joinedAt, NextLevel: 1, TotalLevels: totalLevels,
	})
}

func shouldContinuePlayer(ctx workflow.Context, state *PlayerState) bool {
	return state.ActiveGame == nil && (workflow.GetInfo(ctx).GetContinueAsNewSuggested() ||
		workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged())
}

func continuePlayerAsNew(ctx workflow.Context, state *PlayerState) error {
	options := workflow.ContinueAsNewErrorOptions{}
	if workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
		options.InitialVersioningBehavior = workflow.ContinueAsNewVersioningBehaviorAutoUpgrade
	}
	return workflow.NewContinueAsNewErrorWithOptions(ctx, options, PlayerWorkflowName, PlayerWorkflowInput{State: state})
}

func cloneActiveGame(active *game.ActiveGame) *game.ActiveGame {
	if active == nil {
		return nil
	}
	copy := *active
	return &copy
}

func validateCredentials(state *PlayerState, update AuthenticatePlayerInput) error {
	if update.PasswordHash == "" {
		return temporal.NewApplicationError("password hash is required", "invalid_credentials")
	}
	if state.PasswordHash == "" {
		if !update.Register {
			return temporal.NewApplicationError("account does not exist", "account_not_found")
		}
		if update.DisplayName == "" {
			return temporal.NewApplicationError("display name is required", "invalid_display_name")
		}
		return nil
	}
	if state.PasswordHash != update.PasswordHash {
		return temporal.NewApplicationError("invalid username or password", "invalid_credentials")
	}
	return nil
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

func validateSpendPoints(state *PlayerState, spend SpendPointsInput) error {
	if spend.Amount <= 0 || spend.Reason == "" {
		return temporal.NewApplicationError("point spend must include a positive amount and reason", "invalid_point_spend")
	}
	if state.ActiveGame == nil || state.ActiveGame.WorkflowID != spend.LevelWorkflowID {
		return temporal.NewApplicationError("the level is not active for this player", "level_not_active")
	}
	if state.Points < spend.Amount {
		return temporal.NewApplicationError(
			fmt.Sprintf("not enough points; need %d, have %d", spend.Amount, state.Points),
			"insufficient_points",
		)
	}
	return nil
}

func validateStreakFreezePurchase(state *PlayerState) error {
	if state.StreakFreeze {
		return temporal.NewApplicationError("a streak freeze is already ready", "streak_freeze_owned")
	}
	if state.Points < streakFreezePointCost {
		return temporal.NewApplicationError(
			fmt.Sprintf("not enough points; need %d, have %d", streakFreezePointCost, state.Points),
			"insufficient_points",
		)
	}
	return nil
}
