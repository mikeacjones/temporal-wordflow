package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	querypb "go.temporal.io/api/query/v1"
	"go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"
)

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

func TestWorkflowUIURLWithoutRunID(t *testing.T) {
	t.Parallel()

	got := workflowUIURL(
		"https://cloud.temporal.io/",
		"wordflow.a1b2c",
		"wordflow-campaign/temporal-foundations",
		"",
	)
	want := "https://cloud.temporal.io/namespaces/wordflow.a1b2c/workflows/wordflow-campaign%2Ftemporal-foundations"
	if got != want {
		t.Fatalf("workflowUIURL() = %q, want %q", got, want)
	}
}

func TestIndexWithoutSessionRendersLoginImmediately(t *testing.T) {
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, "test-task-queue", "http://localhost:8233", "default",
		"a-test-session-secret-with-at-least-32-characters")
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Body.String(), `data-session="anonymous"`)
	require.Contains(t, response.Body.String(), `id="startup"`)
	require.Contains(t, response.Body.String(), `id="auth" class="auth-grid"`)
}

func TestIndexWithExpiredSessionRendersLoginImmediately(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now.Add(-2*time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, "test-task-queue", "http://localhost:8233", "default", string(secret))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Cookie", sessionCookieName+"="+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `data-session="anonymous"`)
}

func TestIndexWithValidSessionStartsOnNeutralLoadingScreen(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now, now.Add(time.Hour))
	require.NoError(t, err)
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, "test-task-queue", "http://localhost:8233", "default", string(secret))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Cookie", sessionCookieName+"="+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `data-session="authenticated"`)
}

func TestPlayerResponseContainsOnlyBrowserFields(t *testing.T) {
	response := newPlayerResponse(game.PlayerView{
		PlayerID: "alice", DisplayName: "Alice", CreatedAt: time.Now(), LastSeenAt: time.Now(),
		Points: 25, CompletedLevelCount: 2,
		Campaigns:       []campaign.PlayerCampaignProgress{{CampaignID: "campaign"}},
		CompletedLevels: []game.LevelCompletion{{CampaignID: "campaign", Level: 1}},
		Rewards:         []game.Reward{{CampaignID: "campaign", Points: 10}},
		ActiveGame: &game.ActiveGame{
			WorkflowID: "level/workflow", CampaignID: "campaign", Level: 2, Title: "Title", TotalLevels: 3,
		},
	})

	payload := jsonObject(t, response)
	for _, field := range []string{"playerId", "createdAt", "lastSeenAt", "campaigns", "completedLevels", "rewards"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("player response unexpectedly contains %q", field)
		}
	}
	var active map[string]json.RawMessage
	if err := json.Unmarshal(payload["activeGame"], &active); err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active["campaignId"] == nil || active["level"] == nil {
		t.Fatalf("activeGame fields = %v, want only campaignId and level", active)
	}
}

func TestCampaignResponseOmitsWorkflowOnlyFields(t *testing.T) {
	response := newCatalogCampaignResponse(campaign.View{
		CampaignID: "campaign", WorkflowID: "campaign/workflow",
		Game: campaign.GameSummary{ID: "wordflow"}, Title: "Campaign", Eligible: true,
	}, "https://example.test/workflow")
	payload := jsonObject(t, response)
	for _, field := range []string{"workflowId", "game", "eligible"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("campaign response unexpectedly contains %q", field)
		}
	}
}

func TestGameResponseOmitsWorkflowStateNotRenderedByBrowser(t *testing.T) {
	completedAt := time.Now()
	response := newGameResponse(game.GameView{
		WorkflowID: "level/workflow", PlayerID: "alice", CampaignID: "campaign", Level: 1,
		Words: []game.WordView{{Length: 4}}, CompletedAt: &completedAt,
		Score: &game.GameScore{Points: 10, IncorrectGuesses: 3, HintsUsed: 2},
	})
	payload := jsonObject(t, response)
	for _, field := range []string{"workflowId", "playerId", "words", "completedAt"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("game response unexpectedly contains %q", field)
		}
	}
	var score map[string]json.RawMessage
	if err := json.Unmarshal(payload["score"], &score); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"incorrectGuesses", "hintsUsed"} {
		if _, ok := score[field]; ok {
			t.Fatalf("score response unexpectedly contains %q", field)
		}
	}
}

func TestAuthenticatedPlayerRejectsClosedPlayerWorkflow(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now, now.Add(time.Hour))
	require.NoError(t, err)

	temporalClient := temporalmocks.NewClient(t)
	temporalClient.On("QueryWorkflowWithOptions", mock.Anything, mock.MatchedBy(func(request *client.QueryWorkflowWithOptionsRequest) bool {
		return request.WorkflowID == workflows.PlayerWorkflowID("alice") &&
			request.QueryType == workflows.QueryPlayerState &&
			request.QueryRejectCondition == enums.QUERY_REJECT_CONDITION_NOT_OPEN
	})).Return(&client.QueryWorkflowWithOptionsResponse{
		QueryRejected: &querypb.QueryRejected{Status: enums.WORKFLOW_EXECUTION_STATUS_TERMINATED},
	}, nil).Once()

	request := httptest.NewRequest("GET", "/api/session", nil)
	request.Header.Set("Cookie", "wordflow_session="+token)
	server := &Server{temporal: temporalClient, sessionJWTSecret: secret}

	response := httptest.NewRecorder()
	server.openSession(response, request)
	require.Equal(t, 401, response.Code)
	require.Contains(t, response.Body.String(), "session is invalid or expired")
}

func TestAuthenticatedPlayerAcceptsOpenPlayerWorkflow(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now, now.Add(time.Hour))
	require.NoError(t, err)

	result := temporalmocks.NewEncodedValue(t)
	result.On("Get", mock.Anything).Run(func(arguments mock.Arguments) {
		player := arguments.Get(0).(*game.PlayerView)
		*player = game.PlayerView{PlayerID: "alice", DisplayName: "Alice"}
	}).Return(nil).Once()
	temporalClient := temporalmocks.NewClient(t)
	temporalClient.On("QueryWorkflowWithOptions", mock.Anything, mock.MatchedBy(func(request *client.QueryWorkflowWithOptionsRequest) bool {
		return request.WorkflowID == workflows.PlayerWorkflowID("alice") &&
			request.QueryType == workflows.QueryPlayerState &&
			request.QueryRejectCondition == enums.QUERY_REJECT_CONDITION_NOT_OPEN
	})).
		Return(&client.QueryWorkflowWithOptionsResponse{QueryResult: result}, nil).Once()

	request := httptest.NewRequest("GET", "/api/session", nil)
	request.Header.Set("Cookie", "wordflow_session="+token)
	server := &Server{temporal: temporalClient, sessionJWTSecret: secret}

	player, err := server.authenticatedPlayer(request)
	require.NoError(t, err)
	require.Equal(t, "alice", player.PlayerID)
}

func jsonObject(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	return object
}
