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
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	temporal          client.Client
	taskQueue         string
	temporalUIURL     string
	temporalNamespace string
}

func New(temporalClient client.Client, taskQueue, temporalUIURL, temporalNamespace string) http.Handler {
	server := &Server{
		temporal:          temporalClient,
		taskQueue:         taskQueue,
		temporalUIURL:     temporalUIURL,
		temporalNamespace: temporalNamespace,
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
	mux.HandleFunc("GET /api/campaigns/{campaign}/workflow-link", server.getCampaignWorkflowLink)
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

	view, rawToken, expiresAt, err := s.openPlayerSession(request.Context(), username, displayName,
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
	setSessionCookie(writer, request, username, rawToken, expiresAt)
	writeJSON(writer, http.StatusCreated, view)
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

	view, rawToken, expiresAt, err := s.openPlayerSession(request.Context(), username, "",
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
	setSessionCookie(writer, request, username, rawToken, expiresAt)
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) openSession(writer http.ResponseWriter, request *http.Request) {
	playerID, rawToken, err := sessionCookie(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	var view game.PlayerView
	err = s.update(request.Context(), workflows.PlayerWorkflowID(playerID), newID(), workflows.UpdateResumeSession,
		workflows.ResumeSessionInput{TokenHash: sessionTokenHash(rawToken)}, &view)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, errors.New("session is invalid or expired"))
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) openPlayerSession(ctx context.Context, username, displayName, passwordHash, requestID string,
	register bool,
) (game.PlayerView, string, time.Time, error) {
	rawToken, tokenHash := sessionToken(passwordHash, requestID)
	expiresAt := time.Now().Add(sessionLifetime)
	update := workflows.OpenSessionInput{
		DisplayName: displayName, PasswordHash: passwordHash,
		TokenHash: tokenHash, ExpiresAt: expiresAt, Register: register,
	}
	workflowID := workflows.PlayerWorkflowID(username)
	var view game.PlayerView

	if !register {
		err := s.update(ctx, workflowID, requestID, workflows.UpdateOpenSession, update, &view)
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
			UpdateID: requestID, UpdateName: workflows.UpdateOpenSession,
			WaitForStage: client.WorkflowUpdateStageCompleted, Args: []any{update},
		},
	})
	if err == nil {
		err = handle.Get(ctx, &view)
	}
	return view, rawToken, expiresAt, err
}

func (s *Server) getPlayer(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}

	result, err := s.temporal.QueryWorkflow(request.Context(), workflows.PlayerWorkflowID(session.PlayerID), "", workflows.QueryPlayerState)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	var view game.PlayerView
	if err := result.Get(&view); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) getPlayerWorkflowLink(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.writeWorkflowLink(writer, request, workflows.PlayerWorkflowID(session.PlayerID))
}

type catalogResponse struct {
	Games     []campaign.GameSummary `json:"games"`
	Campaigns []campaign.View        `json:"campaigns"`
}

func (s *Server) getCatalog(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	player, err := s.queryPlayer(request.Context(), session.PlayerID)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}

	defaultCampaign, err := bootstrap.StartDefaultCampaign(request.Context(), s.temporal, s.taskQueue)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	start := s.temporal.NewWithStartWorkflowOperation(client.StartWorkflowOptions{
		ID:                       workflows.CatalogWorkflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, workflows.CatalogWorkflowName, workflows.CatalogWorkflowInput{
		InitialCampaigns: []campaign.Registration{defaultCampaign},
	})
	handle, err := s.temporal.UpdateWithStartWorkflow(request.Context(), client.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: start,
		UpdateOptions: client.UpdateWorkflowOptions{
			UpdateID: newID(), UpdateName: workflows.UpdateOpenCatalog,
			WaitForStage: client.WorkflowUpdateStageCompleted,
			Args:         []any{[]campaign.Registration{defaultCampaign}},
		},
	})
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	var catalogView campaign.CatalogView
	if err := handle.Get(request.Context(), &catalogView); err != nil {
		writeTemporalError(writer, err)
		return
	}

	response := catalogResponse{Games: catalogView.Games}
	progress := campaignPlayerProgress(player)
	for _, registration := range catalogView.Campaigns {
		result, err := s.temporal.QueryWorkflow(request.Context(), registration.WorkflowID, "",
			workflows.QueryCampaignView, campaign.QueryInput{Player: progress})
		if err != nil {
			writeTemporalError(writer, err)
			return
		}
		var view campaign.View
		if err := result.Get(&view); err != nil {
			writeTemporalError(writer, err)
			return
		}
		response.Campaigns = append(response.Campaigns, view)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getCampaignWorkflowLink(writer http.ResponseWriter, request *http.Request) {
	if _, err := s.authenticatedSession(request); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, ok := requestCampaignID(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid campaign ID"))
		return
	}
	s.writeWorkflowLink(writer, request, workflows.WordflowCampaignWorkflowID(campaignID))
}

func (s *Server) buyStreakFreeze(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
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
	if err := s.update(request.Context(), workflows.PlayerWorkflowID(session.PlayerID), input.RequestID,
		workflows.UpdateBuyStreakFreeze, workflows.BuyStreakFreezeInput{}, &view); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

type startGameRequest struct {
	RequestID string `json:"requestId"`
	Level     int    `json:"level"`
}

func (s *Server) startGame(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
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
	if err := s.update(request.Context(), workflows.PlayerWorkflowID(session.PlayerID), input.RequestID, workflows.UpdateStartLevel,
		workflows.StartLevelInput{CampaignID: campaignID, Level: input.Level}, &view); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) getGame(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, level, ok := requestGame(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid game ID"))
		return
	}

	result, err := s.temporal.QueryWorkflow(request.Context(), workflows.WordflowLevelWorkflowID(session.PlayerID, campaignID, level), "", workflows.QueryWordflowLevelState)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	var view game.GameView
	if err := result.Get(&view); err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) getWordflowLevelWorkflowLink(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	campaignID, level, ok := requestGame(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, errors.New("invalid game ID"))
		return
	}
	s.writeWorkflowLink(writer, request, workflows.WordflowLevelWorkflowID(session.PlayerID, campaignID, level))
}

type guessRequest struct {
	RequestID string `json:"requestId"`
	Word      string `json:"word"`
}

func (s *Server) submitGuess(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
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
	err = s.update(request.Context(), workflows.WordflowLevelWorkflowID(session.PlayerID, campaignID, level), input.RequestID, workflows.UpdateSubmitGuess,
		workflows.SubmitGuessInput{Word: input.Word}, &result)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

type hintRequest struct {
	RequestID string        `json:"requestId"`
	Hint      game.HintType `json:"hint"`
}

func (s *Server) useHint(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
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
	err = s.update(request.Context(), workflows.WordflowLevelWorkflowID(session.PlayerID, campaignID, level), input.RequestID, workflows.UpdateUseHint,
		workflows.UseHintInput{Hint: input.Hint}, &result)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) shuffle(writer http.ResponseWriter, request *http.Request) {
	session, err := s.authenticatedSession(request)
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
	err = s.update(request.Context(), workflows.WordflowLevelWorkflowID(session.PlayerID, campaignID, level), input.RequestID, workflows.UpdateShuffle,
		workflows.ShuffleInput{}, &view)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) authenticatedSession(request *http.Request) (game.SessionView, error) {
	playerID, rawToken, err := sessionCookie(request)
	if err != nil {
		return game.SessionView{}, err
	}
	result, err := s.temporal.QueryWorkflow(request.Context(), workflows.PlayerWorkflowID(playerID), "",
		workflows.QueryPlayerSession, sessionTokenHash(rawToken))
	if err != nil {
		return game.SessionView{}, errors.New("session is invalid or expired")
	}
	var session game.SessionView
	if err := result.Get(&session); err != nil {
		return game.SessionView{}, errors.New("session is invalid or expired")
	}
	return session, nil
}

func (s *Server) logOut(writer http.ResponseWriter, request *http.Request) {
	playerID, rawToken, err := sessionCookie(request)
	if err == nil {
		var ignored any
		_ = s.update(request.Context(), workflows.PlayerWorkflowID(playerID), newID(), workflows.UpdateRevokeSession,
			workflows.RevokeSessionInput{TokenHash: sessionTokenHash(rawToken)}, &ignored)
	}
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
	return fmt.Sprintf("%s/namespaces/%s/workflows/%s/%s/timeline",
		strings.TrimRight(baseURL, "/"),
		url.PathEscape(namespace),
		url.PathEscape(workflowID),
		url.PathEscape(runID),
	)
}

func (s *Server) queryPlayer(ctx context.Context, playerID string) (game.PlayerView, error) {
	result, err := s.temporal.QueryWorkflow(ctx, workflows.PlayerWorkflowID(playerID), "", workflows.QueryPlayerState)
	if err != nil {
		return game.PlayerView{}, err
	}
	var view game.PlayerView
	err = result.Get(&view)
	return view, err
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
