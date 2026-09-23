package explorer

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/mjones/temporal-word-game/internal/workflows"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservicepb "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	listPageSize    = 100
	historyPageSize = 200
	maxPageToken    = 16 * 1024
)

//go:embed web/*
var webFiles embed.FS

type workflowReader interface {
	ListWorkflow(context.Context, *workflowservicepb.ListWorkflowExecutionsRequest) (*workflowservicepb.ListWorkflowExecutionsResponse, error)
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservicepb.DescribeWorkflowExecutionResponse, error)
	GetWorkflowHistory(context.Context, *workflowservicepb.GetWorkflowExecutionHistoryRequest) (*workflowservicepb.GetWorkflowExecutionHistoryResponse, error)
}

type temporalReader struct {
	client    client.Client
	namespace string
}

func (r temporalReader) ListWorkflow(ctx context.Context, request *workflowservicepb.ListWorkflowExecutionsRequest) (*workflowservicepb.ListWorkflowExecutionsResponse, error) {
	request.Namespace = r.namespace
	return r.client.ListWorkflow(ctx, request)
}

func (r temporalReader) DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservicepb.DescribeWorkflowExecutionResponse, error) {
	return r.client.DescribeWorkflowExecution(ctx, workflowID, runID)
}

func (r temporalReader) GetWorkflowHistory(ctx context.Context, request *workflowservicepb.GetWorkflowExecutionHistoryRequest) (*workflowservicepb.GetWorkflowExecutionHistoryResponse, error) {
	request.Namespace = r.namespace
	return r.client.WorkflowService().GetWorkflowExecutionHistory(ctx, request)
}

type Server struct {
	reader       workflowReader
	authenticate func(*http.Request) (string, error)
}

type viewer struct {
	Authenticated bool   `json:"authenticated"`
	PlayerID      string `json:"playerId,omitempty"`
}

type workflowListResponse struct {
	Viewer        viewer            `json:"viewer"`
	Workflows     []workflowSummary `json:"workflows"`
	NextPageToken string            `json:"nextPageToken,omitempty"`
}

type workflowDetailResponse struct {
	Workflow      workflowSummary `json:"workflow"`
	Pending       pendingSummary  `json:"pending"`
	Events        []eventView     `json:"events"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
}

type workflowSummary struct {
	WorkflowID           string     `json:"workflowId"`
	RunID                string     `json:"runId"`
	Type                 string     `json:"type"`
	Kind                 string     `json:"kind"`
	Visibility           string     `json:"visibility"`
	Status               string     `json:"status"`
	StartedAt            *time.Time `json:"startedAt,omitempty"`
	ClosedAt             *time.Time `json:"closedAt,omitempty"`
	HistoryLength        int64      `json:"historyLength"`
	HistorySizeBytes     int64      `json:"historySizeBytes"`
	StateTransitionCount int64      `json:"stateTransitionCount"`
	VersioningBehavior   string     `json:"versioningBehavior,omitempty"`
	WorkerDeployment     string     `json:"workerDeployment,omitempty"`
	WorkerBuildID        string     `json:"workerBuildId,omitempty"`
	HasParent            bool       `json:"hasParent"`
}

type pendingSummary struct {
	Activities int `json:"activities"`
	Children   int `json:"children"`
	Nexus      int `json:"nexus"`
}

type eventView struct {
	ID            int64              `json:"id"`
	Time          *time.Time         `json:"time,omitempty"`
	Type          string             `json:"type"`
	Category      string             `json:"category"`
	Title         string             `json:"title"`
	Detail        string             `json:"detail,omitempty"`
	Details       map[string]any     `json:"details"`
	ChildWorkflow *workflowReference `json:"childWorkflow,omitempty"`
}

type workflowReference struct {
	WorkflowID string `json:"workflowId"`
	RunID      string `json:"runId"`
	Type       string `json:"type"`
	Kind       string `json:"kind"`
}

func New(temporalClient client.Client, namespace string, authenticate func(*http.Request) (string, error)) http.Handler {
	return newHandler(temporalReader{client: temporalClient, namespace: namespace}, authenticate)
}

func newHandler(reader workflowReader, authenticate func(*http.Request) (string, error)) http.Handler {
	server := &Server{reader: reader, authenticate: authenticate}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/workflows", server.listWorkflows)
	mux.HandleFunc("GET /api/workflow", server.getWorkflow)

	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		panic(err)
	}
	mux.HandleFunc("GET /{$}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(index)
	})
	staticHandler := http.FileServer(http.FS(static))
	mux.Handle("/", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "public, max-age=3600")
		staticHandler.ServeHTTP(writer, request)
	}))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) listWorkflows(writer http.ResponseWriter, request *http.Request) {
	pageToken, err := decodePageToken(request.URL.Query().Get("page"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	currentViewer := s.viewer(request)
	response, err := s.reader.ListWorkflow(request.Context(), &workflowservicepb.ListWorkflowExecutionsRequest{
		PageSize: listPageSize, NextPageToken: pageToken, Query: listFilter(currentViewer.PlayerID),
	})
	if err != nil {
		writeTemporalError(writer, err)
		return
	}

	workflows := make([]workflowSummary, 0, len(response.GetExecutions()))
	for _, execution := range response.GetExecutions() {
		workflowID := execution.GetExecution().GetWorkflowId()
		if !canViewWorkflow(currentViewer.PlayerID, workflowID, execution.GetStatus()) {
			continue
		}
		workflows = append(workflows, summarizeWorkflow(execution, workflowID))
	}
	writeJSON(writer, http.StatusOK, workflowListResponse{
		Viewer: currentViewer, Workflows: workflows, NextPageToken: encodePageToken(response.GetNextPageToken()),
	})
}

func (s *Server) getWorkflow(writer http.ResponseWriter, request *http.Request) {
	workflowID := request.URL.Query().Get("workflowId")
	runID := request.URL.Query().Get("runId")
	currentViewer := s.viewer(request)
	if !canViewWorkflowID(currentViewer.PlayerID, workflowID) {
		writeError(writer, http.StatusForbidden, errors.New("workflow is not available to this viewer"))
		return
	}
	if len(runID) > 128 || strings.ContainsAny(runID, "\r\n\x00") {
		writeError(writer, http.StatusBadRequest, errors.New("invalid run ID"))
		return
	}
	pageToken, err := decodePageToken(request.URL.Query().Get("historyPage"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}

	description, err := s.reader.DescribeWorkflowExecution(request.Context(), workflowID, runID)
	if err != nil {
		writeTemporalError(writer, err)
		return
	}
	info := description.GetWorkflowExecutionInfo()
	if info == nil || !canViewWorkflow(currentViewer.PlayerID, info.GetExecution().GetWorkflowId(), info.GetStatus()) {
		writeError(writer, http.StatusForbidden, errors.New("workflow is not available to this viewer"))
		return
	}
	history, err := s.reader.GetWorkflowHistory(request.Context(), &workflowservicepb.GetWorkflowExecutionHistoryRequest{
		Execution:       &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: info.GetExecution().GetRunId()},
		MaximumPageSize: historyPageSize, NextPageToken: pageToken,
		HistoryEventFilterType: enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT,
	})
	if err != nil {
		writeTemporalError(writer, err)
		return
	}

	events := make([]eventView, 0)
	if history.GetHistory() != nil {
		events = make([]eventView, 0, len(history.GetHistory().GetEvents()))
		for _, event := range history.GetHistory().GetEvents() {
			view := sanitizeEvent(event, workflowVisibility(workflowID) == "public")
			if child, status := childWorkflowReference(event); child != nil &&
				canViewWorkflow(currentViewer.PlayerID, child.WorkflowID, status) {
				view.ChildWorkflow = child
			}
			events = append(events, view)
		}
	}
	writeJSON(writer, http.StatusOK, workflowDetailResponse{
		Workflow: summarizeWorkflow(info, workflowID),
		Pending: pendingSummary{
			Activities: len(description.GetPendingActivities()),
			Children:   len(description.GetPendingChildren()),
			Nexus:      len(description.GetPendingNexusOperations()),
		},
		Events: events, NextPageToken: encodePageToken(history.GetNextPageToken()),
	})
}

func (s *Server) viewer(request *http.Request) viewer {
	playerID, err := s.authenticate(request)
	if err != nil {
		return viewer{}
	}
	return viewer{Authenticated: true, PlayerID: playerID}
}

func listFilter(playerID string) string {
	public := []string{
		fmt.Sprintf("WorkflowId = '%s'", workflows.CatalogWorkflowID),
		fmt.Sprintf("WorkflowId = '%s'", workflows.DailyWordflowChallengeWorkflowID),
		fmt.Sprintf("WorkflowId = '%s'", workflows.LeaderboardWorkflowID),
		"WorkflowId STARTS_WITH 'wordflow-campaign/'",
	}
	publicFilter := "(" + strings.Join(public, " OR ") + ")"
	if playerID == "" {
		return "ExecutionStatus != 'Terminated' AND ExecutionStatus != 'Completed' AND " + publicFilter
	}

	owned := []string{
		fmt.Sprintf("WorkflowId = '%s'", workflows.PlayerWorkflowID(playerID)),
		fmt.Sprintf("WorkflowId STARTS_WITH 'wordflow-level/%s/'", playerID),
	}
	ownedFilter := "(" + strings.Join(owned, " OR ") + ")"
	return "ExecutionStatus != 'Terminated' AND ((ExecutionStatus != 'Completed' AND " +
		publicFilter + ") OR " + ownedFilter + ")"
}

func canViewWorkflowID(playerID, workflowID string) bool {
	if workflowID == workflows.CatalogWorkflowID || workflowID == workflows.DailyWordflowChallengeWorkflowID ||
		workflowID == workflows.LeaderboardWorkflowID || strings.HasPrefix(workflowID, "wordflow-campaign/") {
		return true
	}
	return playerOwnsWorkflow(playerID, workflowID)
}

func canViewWorkflow(playerID, workflowID string, status enums.WorkflowExecutionStatus) bool {
	if status == enums.WORKFLOW_EXECUTION_STATUS_TERMINATED {
		return false
	}
	owned := playerOwnsWorkflow(playerID, workflowID)
	if status == enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
		return owned
	}
	return owned || canViewWorkflowID("", workflowID)
}

func playerOwnsWorkflow(playerID, workflowID string) bool {
	if playerID == "" {
		return false
	}
	return workflowID == workflows.PlayerWorkflowID(playerID) ||
		strings.HasPrefix(workflowID, "wordflow-level/"+playerID+"/")
}

func summarizeWorkflow(info *workflowpb.WorkflowExecutionInfo, workflowID string) workflowSummary {
	versioning := info.GetVersioningInfo()
	deployment := versioning.GetDeploymentVersion()
	return workflowSummary{
		WorkflowID:           workflowID,
		RunID:                info.GetExecution().GetRunId(),
		Type:                 safeWorkflowType(info.GetType().GetName()),
		Kind:                 workflowKind(workflowID),
		Visibility:           workflowVisibility(workflowID),
		Status:               cleanEnum(info.GetStatus().String(), "WORKFLOW_EXECUTION_STATUS_"),
		StartedAt:            timestamp(info.GetStartTime()),
		ClosedAt:             timestamp(info.GetCloseTime()),
		HistoryLength:        info.GetHistoryLength(),
		HistorySizeBytes:     info.GetHistorySizeBytes(),
		StateTransitionCount: info.GetStateTransitionCount(),
		VersioningBehavior:   cleanEnum(versioning.GetBehavior().String(), "VERSIONING_BEHAVIOR_"),
		WorkerDeployment:     deployment.GetDeploymentName(),
		WorkerBuildID:        deployment.GetBuildId(),
		HasParent:            info.GetParentExecution() != nil,
	}
}

func workflowKind(workflowID string) string {
	switch {
	case workflowID == workflows.CatalogWorkflowID:
		return "catalog"
	case workflowID == workflows.DailyWordflowChallengeWorkflowID:
		return "daily generator"
	case workflowID == workflows.LeaderboardWorkflowID:
		return "leaderboard"
	case strings.HasPrefix(workflowID, "wordflow-campaign/"):
		return "campaign"
	case strings.HasPrefix(workflowID, "player/"):
		return "player"
	case strings.HasPrefix(workflowID, "wordflow-level/"):
		return "level"
	default:
		return "workflow"
	}
}

func workflowVisibility(workflowID string) string {
	if strings.HasPrefix(workflowID, "player/") || strings.HasPrefix(workflowID, "wordflow-level/") {
		return "private"
	}
	return "public"
}

func safeWorkflowType(name string) string {
	known := []string{
		workflows.CatalogWorkflowName,
		workflows.DailyWordflowChallengeWorkflowName,
		workflows.WordflowCampaignWorkflowName,
		workflows.PlayerWorkflowName,
		workflows.WordflowLevelWorkflowName,
		workflows.LeaderboardWorkflowName,
	}
	if slices.Contains(known, name) {
		return name
	}
	return "Workflow"
}

func safeOperationName(name string) string {
	known := []string{
		workflows.UpdateOpenCatalog,
		workflows.UpdateRegisterCampaign,
		workflows.UpdateUnregisterCampaign,
		workflows.UpdateAuthenticatePlayer,
		workflows.UpdateStartLevel,
		workflows.UpdateSpendPoints,
		workflows.UpdateBuyStreakFreeze,
		workflows.UpdateSubmitGuess,
		workflows.UpdateUseHint,
		workflows.SignalRequestVersionUpgrade,
		workflows.SignalLeaderboardScore,
		workflows.ActivityRegisterCampaign,
		workflows.ActivityUnregisterCampaign,
		workflows.ActivityGenerateDailyWordflowChallenge,
		workflows.ActivityResolveWordflowLevel,
		workflows.ActivitySpendPoints,
		workflows.ActivityPublishLeaderboard,
	}
	if slices.Contains(known, name) {
		return name
	}
	return ""
}

func eventCategory(eventType string) string {
	switch {
	case strings.Contains(eventType, "workflow_task"):
		return "worker"
	case strings.Contains(eventType, "activity"):
		return "activity"
	case strings.Contains(eventType, "timer"):
		return "timer"
	case strings.Contains(eventType, "child_workflow"):
		return "child"
	case strings.Contains(eventType, "update") || strings.Contains(eventType, "signal"):
		return "message"
	case strings.Contains(eventType, "nexus"):
		return "nexus"
	case strings.Contains(eventType, "workflow_execution"):
		return "lifecycle"
	default:
		return "state"
	}
}

func cleanEnum(value, prefix string) string {
	value = strings.TrimPrefix(value, prefix)
	if value == "" {
		return ""
	}
	var normalized strings.Builder
	runes := []rune(value)
	for index, character := range runes {
		if unicode.IsUpper(character) && index > 0 && runes[index-1] != '_' &&
			(unicode.IsLower(runes[index-1]) || (index+1 < len(runes) && unicode.IsLower(runes[index+1]))) {
			normalized.WriteByte('_')
		}
		normalized.WriteRune(unicode.ToLower(character))
	}
	value = normalized.String()
	if value == "unspecified" {
		return ""
	}
	return value
}

func humanize(value string) string {
	words := strings.Fields(strings.ReplaceAll(strings.ToLower(value), "_", " "))
	if len(words) == 0 {
		return "Temporal event"
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

func timestamp(value *timestamppb.Timestamp) *time.Time {
	if value == nil || !value.IsValid() {
		return nil
	}
	t := value.AsTime().UTC()
	return &t
}

func encodePageToken(token []byte) string {
	if len(token) == 0 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(token)
}

func decodePageToken(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > maxPageToken*2 {
		return nil, errors.New("page token is too large")
	}
	token, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(token) > maxPageToken {
		return nil, errors.New("invalid page token")
	}
	return token, nil
}

func writeTemporalError(writer http.ResponseWriter, err error) {
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		writeError(writer, http.StatusNotFound, errors.New("workflow was not found"))
		return
	}
	var invalidArgument *serviceerror.InvalidArgument
	if errors.As(err, &invalidArgument) {
		writeError(writer, http.StatusBadRequest, errors.New("Temporal rejected the request"))
		return
	}
	writeError(writer, http.StatusBadGateway, errors.New("Temporal is temporarily unavailable"))
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
