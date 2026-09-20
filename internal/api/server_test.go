package api

import "testing"

func TestWorkflowUIURL(t *testing.T) {
	t.Parallel()

	got := workflowUIURL(
		"https://cloud.temporal.io/",
		"wordflow.a1b2c",
		"player/42/game/2",
		"run-id",
	)
	want := "https://cloud.temporal.io/namespaces/wordflow.a1b2c/workflows/player%2F42%2Fgame%2F2/run-id/timeline"
	if got != want {
		t.Fatalf("workflowUIURL() = %q, want %q", got, want)
	}
}
