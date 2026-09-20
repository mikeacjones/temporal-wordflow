package workflows

import (
	"sort"

	"github.com/mjones/temporal-word-game/internal/game"

	"go.temporal.io/sdk/workflow"
)

const leaderboardSize = 100

type LeaderboardWorkflowInput struct {
	State *LeaderboardState `json:"state,omitempty"`
}

type LeaderboardState struct {
	Entries             []game.LeaderboardEntry `json:"entries"`
	EventsSinceContinue int                     `json:"eventsSinceContinue"`
	ContinueAfterEvents int                     `json:"continueAfterEvents"`
}

func LeaderboardWorkflow(ctx workflow.Context, input LeaderboardWorkflowInput) error {
	state := input.State
	if state == nil {
		state = &LeaderboardState{ContinueAfterEvents: defaultContinueAfterEvents}
	} else if state.ContinueAfterEvents == 0 {
		state.ContinueAfterEvents = defaultContinueAfterEvents
	}

	if err := workflow.SetQueryHandler(ctx, QueryLeaderboard, func() (game.LeaderboardView, error) {
		entries := make([]game.LeaderboardRank, len(state.Entries))
		for index, entry := range state.Entries {
			entries[index] = game.LeaderboardRank{
				DisplayName: entry.DisplayName, LifetimePointsEarned: entry.LifetimePointsEarned,
				CompletedLevels: entry.CompletedLevels, Rank: index + 1,
			}
		}
		return game.LeaderboardView{Entries: entries}, nil
	}); err != nil {
		return err
	}

	scores := workflow.GetSignalChannel(ctx, SignalLeaderboardScore)
	for {
		var entry game.LeaderboardEntry
		scores.Receive(ctx, &entry)
		upsertLeaderboardEntry(state, entry)
		state.EventsSinceContinue++

		if state.EventsSinceContinue >= state.ContinueAfterEvents ||
			workflow.GetInfo(ctx).GetContinueAsNewSuggested() ||
			workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
			state.EventsSinceContinue = 0
			return continueLeaderboardAsNew(ctx, state)
		}
	}
}

func upsertLeaderboardEntry(state *LeaderboardState, entry game.LeaderboardEntry) {
	for index := range state.Entries {
		if state.Entries[index].PlayerID != entry.PlayerID {
			continue
		}
		if entry.Version <= state.Entries[index].Version {
			return
		}
		state.Entries[index] = entry
		sortLeaderboard(state)
		return
	}

	state.Entries = append(state.Entries, entry)
	sortLeaderboard(state)
	if len(state.Entries) > leaderboardSize {
		state.Entries = state.Entries[:leaderboardSize]
	}
}

func sortLeaderboard(state *LeaderboardState) {
	sort.SliceStable(state.Entries, func(i, j int) bool {
		left, right := state.Entries[i], state.Entries[j]
		if left.LifetimePointsEarned != right.LifetimePointsEarned {
			return left.LifetimePointsEarned > right.LifetimePointsEarned
		}
		if left.CompletedLevels != right.CompletedLevels {
			return left.CompletedLevels > right.CompletedLevels
		}
		return left.PlayerID < right.PlayerID
	})
}

func continueLeaderboardAsNew(ctx workflow.Context, state *LeaderboardState) error {
	options := workflow.ContinueAsNewErrorOptions{}
	if workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
		options.InitialVersioningBehavior = workflow.ContinueAsNewVersioningBehaviorAutoUpgrade
	}
	return workflow.NewContinueAsNewErrorWithOptions(ctx, options, LeaderboardWorkflowName, LeaderboardWorkflowInput{State: state})
}
