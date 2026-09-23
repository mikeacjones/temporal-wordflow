package api

import (
	"bytes"
	"context"
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
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	temporalmocks "go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/temporal"
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
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "games.example.test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Body.String(), `data-session="anonymous"`)
	require.Contains(t, response.Body.String(), `id="startup"`)
	require.Contains(t, response.Body.String(), `id="auth" class="auth-grid"`)
	require.Contains(t, response.Body.String(), `property="og:url" content="https://games.example.test/"`)
	require.Contains(t, response.Body.String(), `property="og:image" content="https://games.example.test/social-preview.png"`)
	require.NotContains(t, response.Body.String(), "{{CANONICAL_URL}}")
}

func TestSocialPreviewImageIsPublic(t *testing.T) {
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, "test-task-queue", "http://localhost:8233", "default",
		"a-test-session-secret-with-at-least-32-characters")
	request := httptest.NewRequest("GET", "/social-preview.png", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 200, response.Code)
	require.Equal(t, "image/png", response.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=3600", response.Header().Get("Cache-Control"))
	require.Greater(t, response.Body.Len(), 1000)
}

func TestWorkflowLinkRedirectDoesNotDescribeWorkflow(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now, now.Add(time.Hour))
	require.NoError(t, err)
	temporalClient := temporalmocks.NewClient(t)
	handler := New(temporalClient, "test-task-queue", "https://temporal.example.test", "wordflow.test", string(secret))
	request := httptest.NewRequest("GET", "/api/me/workflow-link", nil)
	request.Header.Set("Cookie", sessionCookieName+"="+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, 302, response.Code)
	require.Equal(t,
		"https://temporal.example.test/namespaces/wordflow.test/workflows/player%2Falice",
		response.Header().Get("Location"))
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

func TestLoginCredentialFailuresAreIndistinguishable(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *temporalmocks.Client)
	}{
		{
			name: "unknown username",
			setup: func(_ *testing.T, temporalClient *temporalmocks.Client) {
				temporalClient.On("UpdateWorkflow", mock.Anything, mock.Anything).
					Return(nil, serviceerror.NewNotFound("player workflow not found")).Once()
			},
		},
		{
			name: "wrong password",
			setup: func(t *testing.T, temporalClient *temporalmocks.Client) {
				handle := temporalmocks.NewWorkflowUpdateHandle(t)
				handle.On("Get", mock.Anything, mock.Anything).
					Return(temporal.NewApplicationError("invalid username or password", "invalid_credentials")).Once()
				temporalClient.On("UpdateWorkflow", mock.Anything, mock.Anything).Return(handle, nil).Once()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			temporalClient := temporalmocks.NewClient(t)
			test.setup(t, temporalClient)
			server := &Server{
				temporal:         temporalClient,
				sessionJWTSecret: []byte("a-test-session-secret-with-at-least-32-characters"),
			}
			request := httptest.NewRequest("POST", "/api/login", bytes.NewBufferString(
				`{"username":"alice","password":"wrong-password","requestId":"login-test"}`,
			))
			response := httptest.NewRecorder()

			server.logIn(response, request)

			require.Equal(t, 401, response.Code)
			require.JSONEq(t, `{"error":"Invalid username/password combination"}`, response.Body.String())
		})
	}
}

func TestPlayerResponseContainsOnlyBrowserFields(t *testing.T) {
	response := newPlayerResponse(game.PlayerView{
		DisplayName: "Alice", Points: 25, CompletedLevelCount: 2,
		Campaigns: []campaign.PlayerCampaignProgress{{CampaignID: "campaign"}},
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
		Kind: campaign.KindDailyChallenge, Failed: true,
		Game: campaign.GameSummary{ID: "wordflow"}, Title: "Campaign", Eligible: true,
	}, "https://example.test/workflow")
	payload := jsonObject(t, response)
	for _, field := range []string{"workflowId", "game", "eligible"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("campaign response unexpectedly contains %q", field)
		}
	}
	require.NotNil(t, payload["kind"])
	require.NotNil(t, payload["failed"])
}

func TestGameResponseOmitsWorkflowStateNotRenderedByBrowser(t *testing.T) {
	completedAt := time.Now()
	expiresAt := completedAt.Add(time.Minute)
	response := newGameResponse(game.GameView{
		CampaignID: "campaign", Level: 1, TimedOut: true, ExpiresAt: &expiresAt,
		SolutionWords: []game.SolutionWordView{{Answer: "FLOW", Found: false}},
		Score:         &game.GameScore{Points: 10, IncorrectGuesses: 3, HintsUsed: 2},
	})
	payload := jsonObject(t, response)
	for _, field := range []string{"workflowId", "playerId", "words", "completedAt"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("game response unexpectedly contains %q", field)
		}
	}
	require.NotNil(t, payload["timedOut"])
	require.NotNil(t, payload["expiresAt"])
	require.NotNil(t, payload["solutionWords"])
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

func TestCatalogReadsExistingWorkflowWithQuery(t *testing.T) {
	result := temporalmocks.NewEncodedValue(t)
	result.On("Get", mock.Anything).Run(func(arguments mock.Arguments) {
		view := arguments.Get(0).(*campaign.CatalogView)
		*view = campaign.CatalogView{Campaigns: []campaign.View{{
			CampaignID: "campaign", WorkflowID: "wordflow-campaign/campaign", Title: "Campaign",
		}}}
	}).Return(nil).Once()

	temporalClient := temporalmocks.NewClient(t)
	temporalClient.On("QueryWorkflow", mock.Anything, workflows.CatalogWorkflowID, "", workflows.QueryCatalog,
		mock.MatchedBy(func(input campaign.QueryInput) bool { return len(input.Player.Campaigns) == 0 })).
		Return(result, nil).Once()
	server := &Server{
		temporal: temporalClient, temporalUIURL: "https://cloud.temporal.io", temporalNamespace: "wordflow.test",
	}

	response, err := server.catalog(context.Background(), game.PlayerView{})
	require.NoError(t, err)
	require.Len(t, response.Campaigns, 1)
	require.Equal(t, "Campaign", response.Campaigns[0].Title)
	require.Contains(t, response.Campaigns[0].WorkflowURL, "wordflow-campaign%2Fcampaign")
}

func TestStartGameUsesThePlayerUpdateResultWithoutQueryingTheChild(t *testing.T) {
	secret := []byte("a-test-session-secret-with-at-least-32-characters")
	now := time.Now()
	token, err := newSessionToken(secret, "alice", now, now.Add(time.Hour))
	require.NoError(t, err)

	handle := temporalmocks.NewWorkflowUpdateHandle(t)
	handle.On("Get", mock.Anything, mock.Anything).Run(func(arguments mock.Arguments) {
		result := arguments.Get(1).(*workflows.StartLevelResult)
		*result = workflows.StartLevelResult{
			Player: game.PlayerView{DisplayName: "Alice", ActiveGame: &game.ActiveGame{
				CampaignID: "campaign", Level: 1,
			}},
			Game: game.GameView{CampaignID: "campaign", Level: 1, Title: "One", Letters: "ONE"},
		}
	}).Return(nil).Once()
	temporalClient := temporalmocks.NewClient(t)
	temporalClient.On("UpdateWorkflow", mock.Anything, mock.MatchedBy(func(options client.UpdateWorkflowOptions) bool {
		return options.WorkflowID == workflows.PlayerWorkflowID("alice") &&
			options.UpdateName == workflows.UpdateStartLevel
	})).Return(handle, nil).Once()

	request := httptest.NewRequest("POST", "/api/me/campaigns/campaign/levels",
		bytes.NewBufferString(`{"requestId":"start-test","level":1}`))
	request.SetPathValue("campaign", "campaign")
	request.Header.Set("Cookie", sessionCookieName+"="+token)
	response := httptest.NewRecorder()
	server := &Server{temporal: temporalClient, sessionJWTSecret: secret}

	server.startGame(response, request)

	require.Equal(t, 200, response.Code)
	require.JSONEq(t, `{
		"player":{"displayName":"Alice","currentStreak":0,"bestStreak":0,"points":0,
			"lifetimePointsEarned":0,"streakFreeze":false,"streakFreezeCost":0,
			"completedLevelCount":0,"activeGame":{"campaignId":"campaign","level":1}},
		"game":{"campaignId":"campaign","level":1,"title":"One","letters":"ONE","cells":null,
			"foundWords":0,"totalWords":0,"attempts":0,"rejectedWords":null,"speedBonuses":null,
			"hintBonus":0,"accuracyBonus":0,"hints":{"letters":0,"brushes":0,"words":0},
			"hintPrices":{"letter":0,"brush":0,"word":0},"complete":false}
	}`, response.Body.String())
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
		*player = game.PlayerView{DisplayName: "Alice"}
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
	require.Equal(t, "Alice", player.DisplayName)
}

func TestLeaderboardStartsFreshAfterPreviousWorkflowWasTerminated(t *testing.T) {
	result := temporalmocks.NewEncodedValue(t)
	result.On("Get", mock.Anything).Run(func(arguments mock.Arguments) {
		view := arguments.Get(0).(*game.LeaderboardView)
		*view = game.LeaderboardView{Entries: []game.LeaderboardRank{}}
	}).Return(nil).Once()

	temporalClient := temporalmocks.NewClient(t)
	queryRequest := mock.MatchedBy(func(request *client.QueryWorkflowWithOptionsRequest) bool {
		return request.WorkflowID == workflows.LeaderboardWorkflowID &&
			request.QueryType == workflows.QueryLeaderboard &&
			request.QueryRejectCondition == enums.QUERY_REJECT_CONDITION_NOT_OPEN
	})
	temporalClient.On("QueryWorkflowWithOptions", mock.Anything, queryRequest).
		Return(&client.QueryWorkflowWithOptionsResponse{
			QueryRejected: &querypb.QueryRejected{Status: enums.WORKFLOW_EXECUTION_STATUS_TERMINATED},
		}, nil).Once()
	temporalClient.On("ExecuteWorkflow", mock.Anything, mock.MatchedBy(func(options client.StartWorkflowOptions) bool {
		return options.ID == workflows.LeaderboardWorkflowID &&
			options.WorkflowIDConflictPolicy == enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING &&
			options.WorkflowIDReusePolicy == enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY
	}), workflows.LeaderboardWorkflowName, workflows.LeaderboardWorkflowInput{}).
		Return((client.WorkflowRun)(nil), nil).Once()
	temporalClient.On("QueryWorkflowWithOptions", mock.Anything, queryRequest).
		Return(&client.QueryWorkflowWithOptionsResponse{QueryResult: result}, nil).Once()

	request := httptest.NewRequest("GET", "/api/leaderboard", nil)
	response := httptest.NewRecorder()
	server := &Server{temporal: temporalClient, taskQueue: "wordflow"}

	server.getLeaderboard(response, request)

	require.Equal(t, 200, response.Code)
	require.JSONEq(t, `{"entries":[]}`, response.Body.String())
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
