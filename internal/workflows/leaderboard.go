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
	Entries []game.LeaderboardEntry `json:"entries"`
}

func LeaderboardWorkflow(ctx workflow.Context, input LeaderboardWorkflowInput) error {
	state := input.State
	if state == nil {
		state = &LeaderboardState{}
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
	upgrade := workflow.GetSignalChannel(ctx, SignalRequestVersionUpgrade)
	for {
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(scores, func(channel workflow.ReceiveChannel, _ bool) {
			var entry game.LeaderboardEntry
			channel.Receive(ctx, &entry)
			upsertLeaderboardEntry(state, entry)
		})
		selector.AddReceive(upgrade, func(channel workflow.ReceiveChannel, _ bool) {
			var ignored struct{}
			channel.Receive(ctx, &ignored)
		})
		selector.Select(ctx)
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() ||
			workflow.GetInfo(ctx).GetTargetWorkerDeploymentVersionChanged() {
			for {
				var entry game.LeaderboardEntry
				if !scores.ReceiveAsync(&entry) {
					break
				}
				upsertLeaderboardEntry(state, entry)
			}
			return workflow.NewContinueAsNewErrorWithOptions(ctx, workflow.ContinueAsNewErrorOptions{
				InitialVersioningBehavior: workflow.ContinueAsNewVersioningBehaviorAutoUpgrade,
			}, LeaderboardWorkflowName, LeaderboardWorkflowInput{State: state})
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
