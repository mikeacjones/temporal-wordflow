package api

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/campaign"
	"github.com/mjones/temporal-word-game/internal/game"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"golang.org/x/sync/errgroup"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	temporal          client.Client
	taskQueue         string
	temporalUIURL     string
	temporalNamespace string
	sessionJWTSecret  []byte
}

func New(temporalClient client.Client, taskQueue, temporalUIURL, temporalNamespace, sessionJWTSecret string) http.Handler {
	if len(sessionJWTSecret) < 32 {
		panic("SESSION_JWT_SECRET must contain at least 32 characters")
	}
	server := &Server{
		temporal:          temporalClient,
		taskQueue:         taskQueue,
		temporalUIURL:     temporalUIURL,
		temporalNamespace: temporalNamespace,
		sessionJWTSecret:  []byte(sessionJWTSecret),
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
	mux.HandleFunc("POST /api/me/campaigns/{campaign}/levels", server.startGame)
	mux.HandleFunc("GET /api/me/campaigns/{campaign}/levels/{level}", server.getGame)
	mux.HandleFunc("GET /api/me/campaigns/{campaign}/levels/{level}/workflow-link", server.getWordflowLevelWorkflowLink)
	mux.HandleFunc("POST /api/me/campaigns/{campaign}/levels/{level}/guesses", server.submitGuess)
	mux.HandleFunc("POST /api/me/campaigns/{campaign}/levels/{level}/hints", server.useHint)
	mux.HandleFunc("POST /api/me/campaigns/{campaign}/levels/{level}/shuffle", server.shuffle)
	mux.HandleFunc("GET /api/leaderboard", server.getLeaderboard)

	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	leaderboardPage := func(writer http.ResponseWriter, _ *http.Request) {
		page, err := fs.ReadFile(static, "leaderboard.html")
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(page)
	}
	mux.HandleFunc("GET /leaderboard", leaderboardPage)
	mux.HandleFunc("GET /leaderboard/", leaderboardPage)
	mux.Handle("/", http.FileServer(http.FS(static)))
	return mux
}

type credentialsRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`
	RequestID   string `json:"requestId"`
}

func (s *Server) signUp(writer http.ResponseWriter, request *http.Request) {
	var input credentialsRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
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
		writeError(writer, http.StatusUnauthorized, errors.New("invalid username or password"))
		return
	}
	if input.RequestID == "" {
		input.RequestID = newID()
	}
	passwordHash, err := passwordHash(username, input.Password)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, errors.New("invalid username or password"))
		return
	}

	view, rawToken, expiresAt, err := s.authenticatePlayer(request.Context(), username, "",
		passwordHash, input.RequestID, false)
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			writeError(writer, http.StatusNotFound, errors.New("account does not exist"))
			return
		}
		var applicationError *temporal.ApplicationError
		if errors.As(err, &applicationError) {
			writeError(writer, http.StatusUnauthorized, errors.New("invalid username or password"))
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
	s.writeWorkflowLink(writer, request, workflows.PlayerWorkflowID(playerID))
}

type sessionResponse struct {
	Player  playerResponse   `json:"player"`
	Catalog *catalogResponse `json:"catalog,omitempty"`
	Game    *gameResponse    `json:"game,omitempty"`
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
	Title           string               `json:"title"`
	Description     string               `json:"description"`
	Status          campaign.Status      `json:"status"`
	StartsAt        *time.Time           `json:"startsAt,omitempty"`
	EndsAt          *time.Time           `json:"endsAt,omitempty"`
	LockedReason    string               `json:"lockedReason,omitempty"`
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
	CampaignID    string                `json:"campaignId"`
	Level         int                   `json:"level"`
	Title         string                `json:"title"`
	Letters       string                `json:"letters"`
	Cells         []game.CellView       `json:"cells"`
	FoundWords    int                   `json:"foundWords"`
	TotalWords    int                   `json:"totalWords"`
	Attempts      int                   `json:"attempts"`
	RejectedWords []string              `json:"rejectedWords"`
	SpeedBonuses  []game.SpeedBonusTier `json:"speedBonuses"`
	HintBonus     int                   `json:"hintBonus"`
	AccuracyBonus int                   `json:"accuracyBonus"`
	Hints         game.HintInventory    `json:"hints"`
	HintPrices    game.HintPrices       `json:"hintPrices"`
	Complete      bool                  `json:"complete"`
	SpecialEvent  *game.SpecialEvent    `json:"specialEvent,omitempty"`
	Score         *gameScoreResponse    `json:"score,omitempty"`
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
		result, err := s.temporal.QueryWorkflow(ctx, player.ActiveGame.WorkflowID, "", workflows.QueryWordflowLevelState)
		if err != nil {
			return sessionResponse{}, err
		}
		var gameView game.GameView
		if err := result.Get(&gameView); err != nil {
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
	defaultCampaign, err := bootstrap.StartDefaultCampaign(ctx, s.temporal, s.taskQueue)
	if err != nil {
		return catalogResponse{}, err
	}
	start := s.temporal.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
		ID:                       workflows.CatalogWorkflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, workflows.CatalogWorkflowName, workflows.CatalogWorkflowInput{
		InitialCampaigns: []campaign.Registration{defaultCampaign},
	})
	handle, err := s.temporal.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: start,
		UpdateOptions: client.UpdateWorkflowOptions{
			UpdateID: newID(), UpdateName: workflows.UpdateOpenCatalog,
			WaitForStage: client.WorkflowUpdateStageCompleted,
			Args:         []any{[]campaign.Registration{defaultCampaign}},
		},
	})
	if err != nil {
		return catalogResponse{}, err
	}
	var catalogView campaign.CatalogView
	if err := handle.Get(ctx, &catalogView); err != nil {
		return catalogResponse{}, err
	}

	response := catalogResponse{Campaigns: make([]catalogCampaignResponse, len(catalogView.Campaigns))}
	progress := campaignPlayerProgress(player)
	group, queryCtx := errgroup.WithContext(ctx)
	for index, registration := range catalogView.Campaigns {
		index, registration := index, registration
		group.Go(func() error {
			result, err := s.temporal.QueryWorkflow(queryCtx, registration.WorkflowID, "",
				workflows.QueryCampaignView, campaign.QueryInput{Player: progress})
			if err != nil {
				return err
			}
			var view campaign.View
			if err := result.Get(&view); err != nil {
				return err
			}
			response.Campaigns[index] = newCatalogCampaignResponse(view,
				workflowUIURL(s.temporalUIURL, s.temporalNamespace, registration.WorkflowID, ""))
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return catalogResponse{}, err
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

	var view game.PlayerView
	if err := s.update(request.Context(), workflows.PlayerWorkflowID(playerID), input.RequestID, workflows.UpdateStartLevel,
		workflows.StartLevelInput{CampaignID: campaignID, Level: input.Level}, &view); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newPlayerResponse(view))
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

	result, err := s.temporal.QueryWorkflow(request.Context(), workflows.WordflowLevelWorkflowID(playerID, campaignID, level), "", workflows.QueryWordflowLevelState)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	var view game.GameView
	if err := result.Get(&view); err != nil {
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
	s.writeWorkflowLink(writer, request, workflows.WordflowLevelWorkflowID(playerID, campaignID, level))
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

func (s *Server) shuffle(writer http.ResponseWriter, request *http.Request) {
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

	var view game.GameView
	err = s.update(request.Context(), workflows.WordflowLevelWorkflowID(playerID, campaignID, level), input.RequestID, workflows.UpdateShuffle,
		workflows.ShuffleInput{}, &view)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newGameResponse(view))
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
		CampaignID: view.CampaignID, Title: view.Title, Description: view.Description, Status: view.Status,
		StartsAt: view.StartsAt, EndsAt: view.EndsAt, LockedReason: view.LockedReason,
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
		Hints: view.Hints, HintPrices: view.HintPrices, Complete: view.Complete, SpecialEvent: view.SpecialEvent,
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
	result, err := s.temporal.QueryWorkflow(request.Context(), workflows.PlayerWorkflowID(playerID), "",
		workflows.QueryPlayerState)
	if err != nil {
		return game.PlayerView{}, errors.New("session is invalid or expired")
	}
	var player game.PlayerView
	if err := result.Get(&player); err != nil {
		return game.PlayerView{}, errors.New("session is invalid or expired")
	}
	return player, nil
}

func (s *Server) logOut(writer http.ResponseWriter, request *http.Request) {
	clearSessionCookie(writer, request)
	writeJSON(writer, http.StatusOK, map[string]bool{"signedOut": true})
}

func (s *Server) getLeaderboard(writer http.ResponseWriter, request *http.Request) {
	result, err := s.temporal.QueryWorkflow(request.Context(), workflows.LeaderboardWorkflowID, "", workflows.QueryLeaderboard)
	if err != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			writeTemporalError(writer, err)
			return
		}
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
		result, err = s.temporal.QueryWorkflow(request.Context(), workflows.LeaderboardWorkflowID, "", workflows.QueryLeaderboard)
		if err != nil {
			writeTemporalError(writer, err)
			return
		}
	}
	var view game.LeaderboardView
	if err := result.Get(&view); err != nil {
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

func (s *Server) writeWorkflowLink(writer http.ResponseWriter, request *http.Request, workflowID string) {
	description, err := s.temporal.DescribeWorkflowExecution(request.Context(), workflowID, "")
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	runID := description.GetWorkflowExecutionInfo().GetExecution().GetRunId()
	if runID == "" {
		writeError(writer, http.StatusInternalServerError, errors.New("workflow run ID is unavailable"))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{
		"url": workflowUIURL(s.temporalUIURL, s.temporalNamespace, workflowID, runID),
	})
}

func workflowUIURL(baseURL, namespace, workflowID, runID string) string {
	workflowURL := fmt.Sprintf("%s/namespaces/%s/workflows/%s",
		strings.TrimRight(baseURL, "/"),
		url.PathEscape(namespace),
		url.PathEscape(workflowID),
	)
	if runID == "" {
		return workflowURL
	}
	return fmt.Sprintf("%s/%s/timeline", workflowURL, url.PathEscape(runID))
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
