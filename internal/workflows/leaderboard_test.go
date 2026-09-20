package workflows

import (
	"testing"

	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/stretchr/testify/require"
)

func TestLeaderboardUsesAbsoluteLatestScores(t *testing.T) {
	state := &LeaderboardState{}
	upsertLeaderboardEntry(state, game.LeaderboardEntry{
		PlayerID: "one", DisplayName: "Replay", LifetimePointsEarned: 40, CompletedLevels: 1, Version: 1,
	})
	upsertLeaderboardEntry(state, game.LeaderboardEntry{
		PlayerID: "two", DisplayName: "Durable", LifetimePointsEarned: 60, CompletedLevels: 2, Version: 2,
	})
	upsertLeaderboardEntry(state, game.LeaderboardEntry{
		PlayerID: "one", DisplayName: "Replay", LifetimePointsEarned: 80, CompletedLevels: 3, Version: 3,
	})
	upsertLeaderboardEntry(state, game.LeaderboardEntry{
		PlayerID: "one", DisplayName: "Stale", LifetimePointsEarned: 20, CompletedLevels: 1, Version: 1,
	})

	require.Len(t, state.Entries, 2)
	require.Equal(t, "one", state.Entries[0].PlayerID)
	require.Equal(t, 80, state.Entries[0].LifetimePointsEarned)
	require.Equal(t, "Replay", state.Entries[0].DisplayName)
}
