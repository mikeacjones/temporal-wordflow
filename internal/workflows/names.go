package workflows

import (
	"fmt"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue            = "temporal-word-game"
	WorkerDeploymentName = "temporal-word-game"

	CatalogWorkflowName                = "CatalogWorkflow"
	DailyWordflowChallengeWorkflowName = "DailyWordflowChallengeWorkflow"
	WordflowCampaignWorkflowName       = "WordflowCampaignWorkflow"
	PlayerWorkflowName                 = "PlayerWorkflow"
	WordflowLevelWorkflowName          = "WordflowLevelWorkflow"
	LeaderboardWorkflowName            = "LeaderboardWorkflow"

	QueryCampaignSummary             = "campaign-summary"
	QueryCampaignView                = "campaign-view"
	QueryCatalog                     = "catalog"
	QueryCatalogWordflowLevel        = "catalog-wordflow-level"
	QueryDailyWordflowChallengeState = "daily-wordflow-challenge-state"
	QueryWordflowLevel               = "wordflow-level"
	QueryPlayerState                 = "player-state"
	QueryWordflowLevelState          = "wordflow-level-state"
	QueryLeaderboard                 = "leaderboard"

	UpdateOpenCatalog        = "open-catalog"
	UpdateRegisterCampaign   = "register-campaign"
	UpdateUnregisterCampaign = "unregister-campaign"
	// Keep the existing wire name so accounts pinned to older Workers can log in.
	UpdateAuthenticatePlayer = "open-session"
	UpdateStartLevel         = "start-level"
	UpdateSpendPoints        = "spend-points"
	UpdateBuyStreakFreeze    = "buy-streak-freeze"
	UpdateSubmitGuess        = "submit-guess"
	UpdateUseHint            = "use-hint"
	SignalLeaderboardScore   = "record-score"

	ActivityRegisterCampaign               = "RegisterCampaign"
	ActivityUnregisterCampaign             = "UnregisterCampaign"
	ActivityGenerateDailyWordflowChallenge = "GenerateDailyWordflowChallenge"
	ActivityResolveWordflowLevel           = "ResolveWordflowLevel"
	ActivitySpendPoints                    = "SpendPoints"
	ActivityPublishLeaderboard             = "PublishLeaderboard"
)

const (
	CatalogWorkflowID                = "catalog/global"
	DailyWordflowChallengeWorkflowID = "daily-wordflow-challenge/global"
	LeaderboardWorkflowID            = "leaderboard/global"
)

func PlayerWorkflowID(playerID string) string {
	return "player/" + playerID
}

func WordflowCampaignWorkflowID(campaignID string) string {
	return "wordflow-campaign/" + campaignID
}

func WordflowLevelWorkflowID(playerID, campaignID string, level int) string {
	return fmt.Sprintf("wordflow-level/%s/%s/%d", playerID, campaignID, level)
}

// Register uses pinned registrations when Worker Versioning is enabled.
func Register(registry worker.Registry, versioned bool) {
	if !versioned {
		registry.RegisterWorkflowWithOptions(CatalogWorkflow, workflow.RegisterOptions{Name: CatalogWorkflowName})
		registry.RegisterWorkflowWithOptions(DailyWordflowChallengeWorkflow, workflow.RegisterOptions{Name: DailyWordflowChallengeWorkflowName})
		registry.RegisterWorkflowWithOptions(WordflowCampaignWorkflow, workflow.RegisterOptions{Name: WordflowCampaignWorkflowName})
		registry.RegisterWorkflowWithOptions(PlayerWorkflow, workflow.RegisterOptions{Name: PlayerWorkflowName})
		registry.RegisterWorkflowWithOptions(WordflowLevelWorkflow, workflow.RegisterOptions{Name: WordflowLevelWorkflowName})
		registry.RegisterWorkflowWithOptions(LeaderboardWorkflow, workflow.RegisterOptions{Name: LeaderboardWorkflowName})
		return
	}

	registry.RegisterWorkflowWithOptions(CatalogWorkflow, workflow.RegisterOptions{
		Name:               CatalogWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(DailyWordflowChallengeWorkflow, workflow.RegisterOptions{
		Name:               DailyWordflowChallengeWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(WordflowCampaignWorkflow, workflow.RegisterOptions{
		Name:               WordflowCampaignWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(PlayerWorkflow, workflow.RegisterOptions{
		Name:               PlayerWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(WordflowLevelWorkflow, workflow.RegisterOptions{
		Name:               WordflowLevelWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	registry.RegisterWorkflowWithOptions(LeaderboardWorkflow, workflow.RegisterOptions{
		Name:               LeaderboardWorkflowName,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
}
