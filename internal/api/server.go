package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/explorer"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	temporal         client.Client
	taskQueue        string
	sessionJWTSecret []byte
}

func New(temporalClient client.Client, taskQueue, temporalNamespace, sessionJWTSecret string) http.Handler {
	if len(sessionJWTSecret) < 32 {
		panic("SESSION_JWT_SECRET must contain at least 32 characters")
	}
	server := &Server{
		temporal:         temporalClient,
		taskQueue:        taskQueue,
		sessionJWTSecret: []byte(sessionJWTSecret),
	}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/signup", server.signUp)
	mux.HandleFunc("POST /api/login", server.logIn)
	mux.HandleFunc("GET /api/session", server.openSession)
	mux.HandleFunc("DELETE /api/session", server.logOut)
	mux.HandleFunc("GET /api/me", server.getPlayer)
	mux.HandleFunc("GET /api/me/workflow-link", server.getPlayerWorkflowLink)
	mux.HandleFunc("POST /api/me/streak-freeze", server.buyStreakFreeze)
	mux.HandleFunc("GET /api/catalog", server.getCatalog)
	mux.HandleFunc("GET /api/me/campaigns/{campaign}/completion", server.getCompletion)
	mux.HandleFunc("POST /api/me/campaigns/{campaign}/levels", server.startGame)
	mux.HandleFunc("GET /api/me/campaigns/{campaign}/levels/{level}", server.getGame)
	mux.HandleFunc("GET /api/me/campaigns/{campaign}/levels/{level}/workflow-link", server.getWordflowLevelWorkflowLink)
	mux.HandleFunc("POST /api/me/campaigns/{campaign}/levels/{level}/guesses", server.submitGuess)
	mux.HandleFunc("POST /api/me/campaigns/{campaign}/levels/{level}/hints", server.useHint)
	mux.HandleFunc("GET /api/leaderboard", server.getLeaderboard)
	mux.Handle("/workflows/", http.StripPrefix("/workflows", explorer.New(
		temporalClient, temporalNamespace, server.authenticatedPlayerID,
	)))

	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	indexHTML, err := fs.ReadFile(static, "index.html")
	if err != nil {
		panic(err)
	}
	indexPage := func(writer http.ResponseWriter, request *http.Request) {
		sessionState := "anonymous"
		if _, err := sessionCookie(request, server.sessionJWTSecret, time.Now()); err == nil {
			sessionState = "authenticated"
		}
		page := bytes.Replace(indexHTML, []byte(`data-session="unknown"`),
			[]byte(`data-session="`+sessionState+`"`), 1)
		page = renderSocialURLs(page, request, "/")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Vary", "Cookie")
		_, _ = writer.Write(page)
	}
	leaderboardPage := func(writer http.ResponseWriter, request *http.Request) {
		page, err := fs.ReadFile(static, "leaderboard.html")
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		page = renderSocialURLs(page, request, "/leaderboard")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(page)
	}
	mux.HandleFunc("GET /{$}", indexPage)
	mux.HandleFunc("GET /index.html", indexPage)
	mux.HandleFunc("GET /leaderboard", leaderboardPage)
	mux.HandleFunc("GET /leaderboard/", leaderboardPage)
	mux.HandleFunc("GET /leaderboard.html", leaderboardPage)
	staticHandler := http.FileServer(http.FS(static))
	mux.Handle("/", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "public, max-age=3600")
		staticHandler.ServeHTTP(writer, request)
	}))
	return compressResponses(mux)
}

func renderSocialURLs(page []byte, request *http.Request, canonicalPath string) []byte {
	protocol := firstForwardedValue(request.Header.Get("X-Forwarded-Proto"))
	if protocol != "http" && protocol != "https" {
		protocol = "http"
		if request.TLS != nil {
			protocol = "https"
		}
	}
	host := firstForwardedValue(request.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = request.Host
	}
	origin := protocol + "://" + host
	canonicalURL := html.EscapeString(origin + canonicalPath)
	imageURL := html.EscapeString(origin + "/social-preview.png")
	page = bytes.ReplaceAll(page, []byte("{{CANONICAL_URL}}"), []byte(canonicalURL))
	return bytes.ReplaceAll(page, []byte("{{SOCIAL_IMAGE_URL}}"), []byte(imageURL))
}

func firstForwardedValue(value string) string {
	return strings.TrimSpace(strings.Split(value, ",")[0])
}

type credentialsRequest struct {
	Username             string `json:"username"`
	Password             string `json:"password"`
	PasswordConfirmation string `json:"passwordConfirmation,omitempty"`
	DisplayName          string `json:"displayName,omitempty"`
	RequestID            string `json:"requestId"`
}

func (s *Server) signUp(writer http.ResponseWriter, request *http.Request) {
	var input credentialsRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Password != input.PasswordConfirmation {
		writeError(writer, http.StatusBadRequest, errors.New("passwords do not match"))
		return
	}
	username, err := normalizeUsername(input.Username)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	displayName, err := validateDisplayName(input.DisplayName)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	displayName = taggedDisplayName(username, displayName)
	passwordHash, err := passwordHash(username, input.Password)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.RequestID == "" {
		input.RequestID = newID()
	}

	view, rawToken, expiresAt, err := s.authenticatePlayer(request.Context(), username, displayName,
		passwordHash, input.RequestID, true)
	if err != nil {
		var applicationError *temporal.ApplicationError
		if errors.As(err, &applicationError) && applicationError.Type() == "invalid_credentials" {
			writeError(writer, http.StatusConflict, errors.New("username is already registered with a different password"))
			return
		}
		writeTemporalError(writer, err)
		return
	}
	response, err := s.sessionResponse(request.Context(), view)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	setSessionCookie(writer, request, rawToken, expiresAt)
	writeJSON(writer, http.StatusCreated, response)
}

func (s *Server) logIn(writer http.ResponseWriter, request *http.Request) {
	var input credentialsRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	username, err := normalizeUsername(input.Username)
	if err != nil {
		writeInvalidLogin(writer)
		return
	}
	if input.RequestID == "" {
		input.RequestID = newID()
	}
	passwordHash, err := passwordHash(username, input.Password)
	if err != nil {
		writeInvalidLogin(writer)
		return
	}

	view, rawToken, expiresAt, err := s.authenticatePlayer(request.Context(), username, "",
		passwordHash, input.RequestID, false)
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			writeInvalidLogin(writer)
			return
		}
		var applicationError *temporal.ApplicationError
		if errors.As(err, &applicationError) {
			writeInvalidLogin(writer)
			return
		}
		writeTemporalError(writer, err)
		return
	}
	response, err := s.sessionResponse(request.Context(), view)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	setSessionCookie(writer, request, rawToken, expiresAt)
	writeJSON(writer, http.StatusOK, response)
}

func writeInvalidLogin(writer http.ResponseWriter) {
	writeError(writer, http.StatusUnauthorized, errors.New("Invalid username/password combination"))
}

func (s *Server) openSession(writer http.ResponseWriter, request *http.Request) {
	view, err := s.authenticatedPlayer(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, errors.New("session is invalid or expired"))
		return
	}
	response, err := s.sessionResponse(request.Context(), view)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

type authenticatePlayerUpdate struct {
	DisplayName  string `json:"displayName"`
	PasswordHash string `json:"passwordHash"`
	Register     bool   `json:"register"`
}

func (s *Server) authenticatePlayer(ctx context.Context, username, displayName, passwordHash, requestID string,
	register bool,
) (game.PlayerView, string, time.Time, error) {
	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(sessionLifetime)
	rawToken, err := newSessionToken(s.sessionJWTSecret, username, issuedAt, expiresAt)
	if err != nil {
		return game.PlayerView{}, "", time.Time{}, err
	}
	update := authenticatePlayerUpdate{
		DisplayName: displayName, PasswordHash: passwordHash, Register: register,
	}
	workflowID := workflows.PlayerWorkflowID(username)
	var view game.PlayerView

	if !register {
		err := s.update(ctx, workflowID, requestID, workflows.UpdateAuthenticatePlayer, update, &view)
		return view, rawToken, expiresAt, err
	}

	start := s.temporal.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
		ID: workflowID, TaskQueue: s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, workflows.PlayerWorkflowName, workflows.PlayerWorkflowInput{PlayerID: username})
	handle, err := s.temporal.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: start,
		UpdateOptions: client.UpdateWorkflowOptions{
			UpdateID: requestID, UpdateName: workflows.UpdateAuthenticatePlayer,
			WaitForStage: client.WorkflowUpdateStageCompleted, Args: []any{update},
		},
	})
	if err == nil {
		err = handle.Get(ctx, &view)
	}
	return view, rawToken, expiresAt, err
}

func (s *Server) getPlayer(writer http.ResponseWriter, request *http.Request) {
	player, err := s.authenticatedPlayer(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	writeJSON(writer, http.StatusOK, newPlayerResponse(player))
}

func (s *Server) getPlayerWorkflowLink(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.redirectWorkflowLink(writer, request, workflows.PlayerWorkflowID(playerID))
}

type sessionResponse struct {
	Player  playerResponse   `json:"player"`
	Catalog *catalogResponse `json:"catalog,omitempty"`
	Game    *gameResponse    `json:"game,omitempty"`
}

type startGameResponse struct {
	Player playerResponse `json:"player"`
	Game   gameResponse   `json:"game"`
}

type completionResponse struct {
	Player   playerResponse           `json:"player"`
	Campaign *catalogCampaignResponse `json:"campaign,omitempty"`
}

type playerResponse struct {
	DisplayName          string              `json:"displayName"`
	CurrentStreak        int                 `json:"currentStreak"`
	BestStreak           int                 `json:"bestStreak"`
	Points               int                 `json:"points"`
	LifetimePointsEarned int                 `json:"lifetimePointsEarned"`
	StreakFreeze         bool                `json:"streakFreeze"`
	StreakFreezeCost     int                 `json:"streakFreezeCost"`
	CompletedLevelCount  int                 `json:"completedLevelCount"`
	ActiveGame           *activeGameResponse `json:"activeGame,omitempty"`
}

type activeGameResponse struct {
	CampaignID string `json:"campaignId"`
	Level      int    `json:"level"`
}

type catalogCampaignResponse struct {
	CampaignID      string               `json:"campaignId"`
	Kind            campaign.Kind        `json:"kind,omitempty"`
	Title           string               `json:"title"`
	Description     string               `json:"description"`
	Status          campaign.Status      `json:"status"`
	StartsAt        *time.Time           `json:"startsAt,omitempty"`
	EndsAt          *time.Time           `json:"endsAt,omitempty"`
	LockedReason    string               `json:"lockedReason,omitempty"`
	Failed          bool                 `json:"failed,omitempty"`
	NextLevel       int                  `json:"nextLevel"`
	CompletedLevels int                  `json:"completedLevels"`
	TotalLevels     int                  `json:"totalLevels"`
	Levels          []campaign.LevelView `json:"levels"`
	WorkflowURL     string               `json:"workflowUrl"`
}

type catalogResponse struct {
	Campaigns []catalogCampaignResponse `json:"campaigns"`
}

type gameResponse struct {
	CampaignID    string                  `json:"campaignId"`
	Level         int                     `json:"level"`
	Title         string                  `json:"title"`
	Letters       string                  `json:"letters"`
	Cells         []game.CellView         `json:"cells"`
	FoundWords    int                     `json:"foundWords"`
	TotalWords    int                     `json:"totalWords"`
	Attempts      int                     `json:"attempts"`
	RejectedWords []string                `json:"rejectedWords"`
	SpeedBonuses  []game.SpeedBonusTier   `json:"speedBonuses"`
	HintBonus     int                     `json:"hintBonus"`
	AccuracyBonus int                     `json:"accuracyBonus"`
	Hints         game.HintInventory      `json:"hints"`
	HintPrices    game.HintPrices         `json:"hintPrices"`
	Complete      bool                    `json:"complete"`
	TimedOut      bool                    `json:"timedOut,omitempty"`
	ExpiresAt     *time.Time              `json:"expiresAt,omitempty"`
	SpecialEvent  *game.SpecialEvent      `json:"specialEvent,omitempty"`
	Score         *gameScoreResponse      `json:"score,omitempty"`
	SolutionWords []game.SolutionWordView `json:"solutionWords,omitempty"`
}

type gameScoreResponse struct {
	Points          int   `json:"points"`
	BasePoints      int   `json:"basePoints"`
	SpeedBonus      int   `json:"speedBonus"`
	AccuracyBonus   int   `json:"accuracyBonus"`
	HintBonus       int   `json:"hintBonus"`
	DurationSeconds int64 `json:"durationSeconds"`
}

type guessResponse struct {
	Outcome string       `json:"outcome"`
	Game    gameResponse `json:"game"`
}

type hintResponse struct {
	Outcome         string       `json:"outcome"`
	Game            gameResponse `json:"game"`
	PointsSpent     int          `json:"pointsSpent,omitempty"`
	PointsRemaining *int         `json:"pointsRemaining,omitempty"`
}

func (s *Server) getCatalog(writer http.ResponseWriter, request *http.Request) {
	player, err := s.authenticatedPlayer(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	response, err := s.catalog(request.Context(), player)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) sessionResponse(ctx context.Context, player game.PlayerView) (sessionResponse, error) {
	response := sessionResponse{Player: newPlayerResponse(player)}
	if player.ActiveGame != nil {
		gameView, err := s.queryGame(ctx, player.ActiveGame.WorkflowID)
		if err != nil {
			return sessionResponse{}, err
		}
		compactGame := newGameResponse(gameView)
		response.Game = &compactGame
		return response, nil
	}
	catalog, err := s.catalog(ctx, player)
	if err != nil {
		return sessionResponse{}, err
	}
	response.Catalog = &catalog
	return response, nil
}

func (s *Server) catalog(ctx context.Context, player game.PlayerView) (catalogResponse, error) {
	var catalogView campaign.CatalogView
	query := campaign.QueryInput{Player: campaignPlayerProgress(player)}
	queryCatalog := func() (*client.QueryWorkflowWithOptionsResponse, error) {
		return s.temporal.QueryWorkflowWithOptions(ctx, &client.QueryWorkflowWithOptionsRequest{
			WorkflowID:           workflows.CatalogWorkflowID,
			QueryType:            workflows.QueryCatalog,
			Args:                 []any{query},
			QueryRejectCondition: enums.QUERY_REJECT_CONDITION_NOT_OPEN,
		})
	}

	result, err := queryCatalog()
	start := result == nil || result.QueryRejected != nil || result.QueryResult == nil
	if err != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return catalogResponse{}, err
		}
		start = true
	}
	if start {
		defaultCampaign, startErr := bootstrap.StartDefaultCampaign(ctx, s.temporal, s.taskQueue)
		if startErr != nil {
			return catalogResponse{}, startErr
		}
		start := s.temporal.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
			ID:                       workflows.CatalogWorkflowID,
			TaskQueue:                s.taskQueue,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
			WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		}, workflows.CatalogWorkflowName, workflows.CatalogWorkflowInput{
			InitialCampaigns: []workflows.CampaignRegistration{defaultCampaign},
		})
		handle, startErr := s.temporal.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
			StartWorkflowOperation: start,
			UpdateOptions: client.UpdateWorkflowOptions{
				UpdateID: newID(), UpdateName: workflows.UpdateOpenCatalog,
				WaitForStage: client.WorkflowUpdateStageCompleted,
				Args:         []any{[]workflows.CampaignRegistration{defaultCampaign}},
			},
		})
		if startErr != nil {
			return catalogResponse{}, startErr
		}
		var campaignCount int
		if err = handle.Get(ctx, &campaignCount); err == nil {
			result, err = queryCatalog()
		}
	}
	if err != nil {
		return catalogResponse{}, err
	}
	if result == nil || result.QueryRejected != nil || result.QueryResult == nil {
		return catalogResponse{}, errors.New("catalog workflow is not open")
	}
	if err := result.QueryResult.Get(&catalogView); err != nil {
		return catalogResponse{}, err
	}

	response := catalogResponse{Campaigns: make([]catalogCampaignResponse, 0, len(catalogView.Campaigns))}
	for _, view := range catalogView.Campaigns {
		response.Campaigns = append(response.Campaigns, newCatalogCampaignResponse(view,
			workflowExplorerURL(view.WorkflowID, "")))
	}
	return response, nil
}

func (s *Server) buyStreakFreeze(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}

	var input struct {
		RequestID string `json:"requestId"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.RequestID == "" {
		input.RequestID = newID()
	}

	var view game.PlayerView
	if err := s.update(request.Context(), workflows.PlayerWorkflowID(playerID), input.RequestID,
		workflows.UpdateBuyStreakFreeze, workflows.BuyStreakFreezeInput{}, &view); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newPlayerResponse(view))
}

type startGameRequest struct {
	RequestID string `json:"requestId"`
	Level     int    `json:"level"`
}

func (s *Server) getCompletion(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, ok := requestCampaignID(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid campaign ID"))
		return
	}

	deadline := time.Now().Add(5 * time.Second)
	var player game.PlayerView
	for {
		player, err = s.queryPlayer(request.Context(), playerID)
		if err != nil {
			writeError(writer, http.StatusUnauthorized, err)
			return
		}
		if player.ActiveGame == nil {
			break
		}
		if player.ActiveGame.CampaignID != campaignID || time.Now().After(deadline) {
			writeError(writer, http.StatusConflict, errors.New("level completion is still being processed"))
			return
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-request.Context().Done():
			timer.Stop()
			writeError(writer, http.StatusRequestTimeout, request.Context().Err())
			return
		case <-timer.C:
		}
	}

	catalog, err := s.catalog(request.Context(), player)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	var completedCampaign *catalogCampaignResponse
	for index := range catalog.Campaigns {
		if catalog.Campaigns[index].CampaignID == campaignID {
			completedCampaign = &catalog.Campaigns[index]
			break
		}
	}
	writeJSON(writer, http.StatusOK, completionResponse{
		Player:   newPlayerResponse(player),
		Campaign: completedCampaign,
	})
}

func (s *Server) startGame(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}

	var input startGameRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.RequestID == "" {
		input.RequestID = newID()
	}
	campaignID, ok := requestCampaignID(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid campaign ID"))
		return
	}

	var result workflows.StartLevelResult
	if err := s.update(request.Context(), workflows.PlayerWorkflowID(playerID), input.RequestID, workflows.UpdateStartLevel,
		workflows.StartLevelInput{CampaignID: campaignID, Level: input.Level}, &result); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, startGameResponse{
		Player: newPlayerResponse(result.Player),
		Game:   newGameResponse(result.Game),
	})
}

func (s *Server) getGame(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, level, ok := requestGame(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid game ID"))
		return
	}

	view, err := s.queryGame(request.Context(), workflows.WordflowLevelWorkflowID(playerID, campaignID, level))
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newGameResponse(view))
}

func (s *Server) getWordflowLevelWorkflowLink(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, level, ok := requestGame(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid game ID"))
		return
	}
	s.redirectWorkflowLink(writer, request, workflows.WordflowLevelWorkflowID(playerID, campaignID, level))
}

type guessRequest struct {
	RequestID string `json:"requestId"`
	Word      string `json:"word"`
}

func (s *Server) submitGuess(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, level, ok := requestGame(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid game ID"))
		return
	}
	var input guessRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.RequestID == "" {
		input.RequestID = newID()
	}

	var result game.GuessResult
	err = s.update(request.Context(), workflows.WordflowLevelWorkflowID(playerID, campaignID, level), input.RequestID, workflows.UpdateSubmitGuess,
		workflows.SubmitGuessInput{Word: input.Word}, &result)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, guessResponse{Outcome: result.Outcome, Game: newGameResponse(result.Game)})
}

type hintRequest struct {
	RequestID string        `json:"requestId"`
	Hint      game.HintType `json:"hint"`
}

func (s *Server) useHint(writer http.ResponseWriter, request *http.Request) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, level, ok := requestGame(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid game ID"))
		return
	}
	var input hintRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.RequestID == "" {
		input.RequestID = newID()
	}

	var result game.HintResult
	err = s.update(request.Context(), workflows.WordflowLevelWorkflowID(playerID, campaignID, level), input.RequestID, workflows.UpdateUseHint,
		workflows.UseHintInput{Hint: input.Hint}, &result)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, hintResponse{
		Outcome: result.Outcome, Game: newGameResponse(result.Game),
		PointsSpent: result.PointsSpent, PointsRemaining: result.PointsRemaining,
	})
}

func newPlayerResponse(player game.PlayerView) playerResponse {
	response := playerResponse{
		DisplayName: player.DisplayName, CurrentStreak: player.CurrentStreak, BestStreak: player.BestStreak,
		Points: player.Points, LifetimePointsEarned: player.LifetimePointsEarned,
		StreakFreeze: player.StreakFreeze, StreakFreezeCost: player.StreakFreezeCost,
		CompletedLevelCount: player.CompletedLevelCount,
	}
	if player.ActiveGame != nil {
		response.ActiveGame = &activeGameResponse{CampaignID: player.ActiveGame.CampaignID, Level: player.ActiveGame.Level}
	}
	return response
}

func newCatalogCampaignResponse(view campaign.View, workflowURL string) catalogCampaignResponse {
	return catalogCampaignResponse{
		CampaignID: view.CampaignID, Kind: view.Kind, Title: view.Title, Description: view.Description, Status: view.Status,
		StartsAt: view.StartsAt, EndsAt: view.EndsAt, LockedReason: view.LockedReason, Failed: view.Failed,
		NextLevel: view.NextLevel, CompletedLevels: view.CompletedLevels, TotalLevels: view.TotalLevels,
		Levels: view.Levels, WorkflowURL: workflowURL,
	}
}

func newGameResponse(view game.GameView) gameResponse {
	response := gameResponse{
		CampaignID: view.CampaignID, Level: view.Level, Title: view.Title, Letters: view.Letters,
		Cells: view.Cells, FoundWords: view.FoundWords, TotalWords: view.TotalWords, Attempts: view.Attempts,
		RejectedWords: view.RejectedWords, SpeedBonuses: view.SpeedBonuses,
		HintBonus: view.HintBonus, AccuracyBonus: view.AccuracyBonus,
		Hints: view.Hints, HintPrices: view.HintPrices, Complete: view.Complete,
		TimedOut: view.TimedOut, ExpiresAt: view.ExpiresAt, SpecialEvent: view.SpecialEvent,
		SolutionWords: view.SolutionWords,
	}
	if view.Score != nil {
		response.Score = &gameScoreResponse{
			Points: view.Score.Points, BasePoints: view.Score.BasePoints, SpeedBonus: view.Score.SpeedBonus,
			AccuracyBonus: view.Score.AccuracyBonus, HintBonus: view.Score.HintBonus,
			DurationSeconds: view.Score.DurationSeconds,
		}
	}
	return response
}

func (s *Server) authenticatedPlayerID(request *http.Request) (string, error) {
	return sessionCookie(request, s.sessionJWTSecret, time.Now())
}

func (s *Server) authenticatedPlayer(request *http.Request) (game.PlayerView, error) {
	playerID, err := s.authenticatedPlayerID(request)
	if err != nil {
		return game.PlayerView{}, err
	}
	return s.queryPlayer(request.Context(), playerID)
}

func (s *Server) queryPlayer(ctx context.Context, playerID string) (game.PlayerView, error) {
	response, err := s.temporal.QueryWorkflowWithOptions(ctx, &client.QueryWorkflowWithOptionsRequest{
		WorkflowID:           workflows.PlayerWorkflowID(playerID),
		QueryType:            workflows.QueryPlayerState,
		QueryRejectCondition: enums.QUERY_REJECT_CONDITION_NOT_OPEN,
	})
	if err != nil || response == nil || response.QueryRejected != nil || response.QueryResult == nil {
		return game.PlayerView{}, errors.New("session is invalid or expired")
	}
	var player game.PlayerView
	if err := response.QueryResult.Get(&player); err != nil {
		return game.PlayerView{}, errors.New("session is invalid or expired")
	}
	return player, nil
}

func (s *Server) queryGame(ctx context.Context, workflowID string) (game.GameView, error) {
	result, err := s.temporal.QueryWorkflow(ctx, workflowID, "", workflows.QueryWordflowLevelState)
	if err != nil {
		return game.GameView{}, err
	}
	var view game.GameView
	if err := result.Get(&view); err != nil {
		return game.GameView{}, err
	}
	return view, nil
}

func (s *Server) logOut(writer http.ResponseWriter, request *http.Request) {
	clearSessionCookie(writer, request)
	writeJSON(writer, http.StatusOK, map[string]bool{"signedOut": true})
}

func (s *Server) getLeaderboard(writer http.ResponseWriter, request *http.Request) {
	query := func() (*client.QueryWorkflowWithOptionsResponse, error) {
		return s.temporal.QueryWorkflowWithOptions(request.Context(), &client.QueryWorkflowWithOptionsRequest{
			WorkflowID:           workflows.LeaderboardWorkflowID,
			QueryType:            workflows.QueryLeaderboard,
			QueryRejectCondition: enums.QUERY_REJECT_CONDITION_NOT_OPEN,
		})
	}

	response, err := query()
	start := response == nil || response.QueryRejected != nil || response.QueryResult == nil
	if err != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			writeTemporalError(writer, err)
			return
		}
		start = true
	}
	if start {
		_, err = s.temporal.ExecuteWorkflow(request.Context(), client.StartWorkflowOptions{
			ID:                       workflows.LeaderboardWorkflowID,
			TaskQueue:                s.taskQueue,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
			WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		}, workflows.LeaderboardWorkflowName, workflows.LeaderboardWorkflowInput{})
		if err != nil {
			writeTemporalError(writer, err)
			return
		}
		response, err = query()
		if err != nil {
			writeTemporalError(writer, err)
			return
		}
	}
	if response == nil || response.QueryRejected != nil || response.QueryResult == nil {
		writeTemporalError(writer, errors.New("leaderboard workflow is not open"))
		return
	}
	var view game.LeaderboardView
	if err := response.QueryResult.Get(&view); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) update(ctx context.Context, workflowID, updateID, updateName string, input, output any) error {
	handle, err := s.temporal.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID: workflowID, UpdateID: updateID, UpdateName: updateName,
		WaitForStage: client.WorkflowUpdateStageCompleted, Args: []any{input},
	})
	if err != nil {
		return err
	}
	return handle.Get(ctx, output)
}

func (s *Server) redirectWorkflowLink(writer http.ResponseWriter, request *http.Request, workflowID string) {
	http.Redirect(writer, request, workflowExplorerURL(workflowID, ""), http.StatusFound)
}

func workflowExplorerURL(workflowID, runID string) string {
	query := url.Values{"workflowId": []string{workflowID}}
	if runID != "" {
		query.Set("runId", runID)
	}
	return "/workflows/?" + query.Encode()
}

func campaignPlayerProgress(player game.PlayerView) campaign.PlayerProgress {
	progress := campaign.PlayerProgress{
		Campaigns: append([]campaign.PlayerCampaignProgress(nil), player.Campaigns...),
	}
	if player.ActiveGame != nil {
		progress.ActiveCampaignID = player.ActiveGame.CampaignID
		progress.ActiveLevel = player.ActiveGame.Level
	}
	return progress
}

func requestCampaignID(request *http.Request) (string, bool) {
	value := request.PathValue("campaign")
	if value == "" || len(value) > 64 {
		return "", false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return "", false
		}
	}
	return value, true
}

func requestGame(request *http.Request) (string, int, bool) {
	campaignID, ok := requestCampaignID(request)
	if !ok {
		return "", 0, false
	}
	level, err := strconv.Atoi(request.PathValue("level"))
	return campaignID, level, err == nil && level > 0
}

func newID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes)
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func writeTemporalError(writer http.ResponseWriter, err error) {
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		writeError(writer, http.StatusNotFound, errors.New("workflow not found"))
		return
	}
	var applicationError *temporal.ApplicationError
	if errors.As(err, &applicationError) {
		writeError(writer, http.StatusConflict, errors.New(applicationError.Message()))
		return
	}
	writeError(writer, http.StatusInternalServerError, err)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
