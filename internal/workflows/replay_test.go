package workflows

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestReplayLiveHistories(t *testing.T) {
	directory := os.Getenv("WORDFLOW_REPLAY_HISTORY_DIR")
	if directory == "" {
		t.Skip("WORDFLOW_REPLAY_HISTORY_DIR is not set")
	}

	files, err := filepath.Glob(filepath.Join(directory, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	sort.Strings(files)

	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(CatalogWorkflow, workflow.RegisterOptions{Name: CatalogWorkflowName})
	replayer.RegisterWorkflowWithOptions(DailyWordflowChallengeWorkflow, workflow.RegisterOptions{Name: DailyWordflowChallengeWorkflowName})
	replayer.RegisterWorkflowWithOptions(WordflowCampaignWorkflow, workflow.RegisterOptions{Name: WordflowCampaignWorkflowName})
	replayer.RegisterWorkflowWithOptions(PlayerWorkflow, workflow.RegisterOptions{Name: PlayerWorkflowName})
	replayer.RegisterWorkflowWithOptions(WordflowLevelWorkflow, workflow.RegisterOptions{Name: WordflowLevelWorkflowName})
	replayer.RegisterWorkflowWithOptions(LeaderboardWorkflow, workflow.RegisterOptions{Name: LeaderboardWorkflowName})

	for _, file := range files {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			require.NoError(t, replayer.ReplayWorkflowHistoryFromJSONFile(nil, file))
		})
	}
}
