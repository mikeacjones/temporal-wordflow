package explorer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mjones/temporal-word-game/internal/workflows"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	updatepb "go.temporal.io/api/update/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservicepb "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeReader struct {
	listResponse     *workflowservicepb.ListWorkflowExecutionsResponse
	description      *workflowservicepb.DescribeWorkflowExecutionResponse
	historyResponse  *workflowservicepb.GetWorkflowExecutionHistoryResponse
	listRequest      *workflowservicepb.ListWorkflowExecutionsRequest
	historyRequest   *workflowservicepb.GetWorkflowExecutionHistoryRequest
	describeRequests int
}

func (f *fakeReader) ListWorkflow(_ context.Context, request *workflowservicepb.ListWorkflowExecutionsRequest) (*workflowservicepb.ListWorkflowExecutionsResponse, error) {
	f.listRequest = request
	return f.listResponse, nil
}

func (f *fakeReader) DescribeWorkflowExecution(_ context.Context, _, _ string) (*workflowservicepb.DescribeWorkflowExecutionResponse, error) {
	f.describeRequests++
	return f.description, nil
}

func (f *fakeReader) GetWorkflowHistory(_ context.Context, request *workflowservicepb.GetWorkflowExecutionHistoryRequest) (*workflowservicepb.GetWorkflowExecutionHistoryResponse, error) {
	f.historyRequest = request
	return f.historyResponse, nil
}

func TestAnonymousListContainsOnlyPublicWorkflows(t *testing.T) {
	runningCampaign := executionInfo(workflows.WordflowCampaignWorkflowID("today"), workflows.WordflowCampaignWorkflowName)
	completedCampaign := executionInfo(workflows.WordflowCampaignWorkflowID("yesterday"), workflows.WordflowCampaignWorkflowName)
	completedCampaign.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	terminatedCampaign := executionInfo(workflows.WordflowCampaignWorkflowID("old"), workflows.WordflowCampaignWorkflowName)
	terminatedCampaign.Status = enums.WORKFLOW_EXECUTION_STATUS_TERMINATED
	reader := &fakeReader{listResponse: &workflowservicepb.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{
		runningCampaign,
		completedCampaign,
		terminatedCampaign,
		executionInfo(workflows.PlayerWorkflowID("bob"), workflows.PlayerWorkflowName),
	}}}
	handler := newHandler(reader, func(*http.Request) (string, error) {
		return "", errors.New("not signed in")
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/workflows", nil))

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, reader.listRequest.GetQuery(), "wordflow-campaign/")
	require.NotContains(t, reader.listRequest.GetQuery(), "player/")
	require.Contains(t, reader.listRequest.GetQuery(), "ExecutionStatus != 'Terminated'")
	require.Contains(t, reader.listRequest.GetQuery(), "ExecutionStatus != 'Completed'")
	require.Contains(t, response.Body.String(), "wordflow-campaign/today")
	require.NotContains(t, response.Body.String(), "wordflow-campaign/yesterday")
	require.NotContains(t, response.Body.String(), "wordflow-campaign/old")
	require.NotContains(t, response.Body.String(), "player/bob")
}

func TestAuthenticatedListIsLimitedToThatPlayer(t *testing.T) {
	completedLevel := executionInfo(workflows.WordflowLevelWorkflowID("alice", "campaign", 1), workflows.WordflowLevelWorkflowName)
	completedLevel.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	terminatedLevel := executionInfo(workflows.WordflowLevelWorkflowID("alice", "campaign", 2), workflows.WordflowLevelWorkflowName)
	terminatedLevel.Status = enums.WORKFLOW_EXECUTION_STATUS_TERMINATED
	completedCampaign := executionInfo(workflows.WordflowCampaignWorkflowID("yesterday"), workflows.WordflowCampaignWorkflowName)
	completedCampaign.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	reader := &fakeReader{listResponse: &workflowservicepb.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{
		executionInfo(workflows.PlayerWorkflowID("alice"), workflows.PlayerWorkflowName),
		completedLevel,
		terminatedLevel,
		completedCampaign,
		executionInfo(workflows.WordflowCampaignWorkflowID("today"), workflows.WordflowCampaignWorkflowName),
		executionInfo(workflows.PlayerWorkflowID("bob"), workflows.PlayerWorkflowName),
	}}}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "alice", nil })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/workflows", nil))

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, reader.listRequest.GetQuery(), "player/alice")
	require.Contains(t, reader.listRequest.GetQuery(), "wordflow-level/alice/")
	require.Contains(t, reader.listRequest.GetQuery(), "ExecutionStatus != 'Terminated'")
	require.Contains(t, response.Body.String(), "player/alice")
	require.Contains(t, response.Body.String(), "wordflow-level/alice/campaign/1")
	require.NotContains(t, response.Body.String(), "wordflow-level/alice/campaign/2")
	require.NotContains(t, response.Body.String(), "wordflow-campaign/yesterday")
	require.Contains(t, response.Body.String(), "wordflow-campaign/today")
	require.NotContains(t, response.Body.String(), "player/bob")
}

func TestTerminatedWorkflowDetailIsUnavailable(t *testing.T) {
	info := executionInfo(workflows.WordflowLevelWorkflowID("alice", "campaign", 1), workflows.WordflowLevelWorkflowName)
	info.Status = enums.WORKFLOW_EXECUTION_STATUS_TERMINATED
	reader := &fakeReader{description: &workflowservicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info}}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "alice", nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/workflow?workflowId="+url.QueryEscape(info.GetExecution().GetWorkflowId()), nil)

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusForbidden, response.Code)
	require.Nil(t, reader.historyRequest)
}

func TestCompletedPublicWorkflowDetailIsUnavailable(t *testing.T) {
	info := executionInfo(workflows.WordflowCampaignWorkflowID("yesterday"), workflows.WordflowCampaignWorkflowName)
	info.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	reader := &fakeReader{description: &workflowservicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info}}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "alice", nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/workflow?workflowId="+url.QueryEscape(info.GetExecution().GetWorkflowId()), nil)

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusForbidden, response.Code)
	require.Nil(t, reader.historyRequest)
}

func TestCompletedPlayerLevelDetailRemainsVisible(t *testing.T) {
	info := executionInfo(workflows.WordflowLevelWorkflowID("alice", "campaign", 1), workflows.WordflowLevelWorkflowName)
	info.Status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	reader := &fakeReader{
		description:     &workflowservicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info},
		historyResponse: &workflowservicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{}},
	}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "alice", nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/workflow?workflowId="+url.QueryEscape(info.GetExecution().GetWorkflowId()), nil)

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.NotNil(t, reader.historyRequest)
	require.Contains(t, response.Body.String(), `"status":"completed"`)
}

func TestAnotherPlayersWorkflowIsRejectedBeforeTemporalIsCalled(t *testing.T) {
	reader := &fakeReader{}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "alice", nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/workflow?workflowId="+url.QueryEscape("player/bob"), nil)
	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusForbidden, response.Code)
	require.Zero(t, reader.describeRequests)
}

func TestHistoryPayloadsAreSanitizedBeforeSerialization(t *testing.T) {
	workflowID := workflows.PlayerWorkflowID("alice")
	runID := "run-one"
	secretPayload := &commonpb.Payloads{Payloads: []*commonpb.Payload{{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte(`{"campaignTitle":"Safe title","passwordHash":"pbkdf2-super-secret","words":["SECRET"]}`),
	}}}
	info := executionInfo(workflowID, workflows.PlayerWorkflowName)
	info.Execution.RunId = runID
	reader := &fakeReader{
		description: &workflowservicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info},
		historyResponse: &workflowservicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{
			{
				EventId: 1, EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
				Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
					WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
						WorkflowType: &commonpb.WorkflowType{Name: workflows.PlayerWorkflowName}, Input: secretPayload,
					},
				},
			},
			{
				EventId: 2, EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_UPDATE_ACCEPTED,
				Attributes: &historypb.HistoryEvent_WorkflowExecutionUpdateAcceptedEventAttributes{
					WorkflowExecutionUpdateAcceptedEventAttributes: &historypb.WorkflowExecutionUpdateAcceptedEventAttributes{
						AcceptedRequest: &updatepb.Request{Input: &updatepb.Input{Name: workflows.UpdateAuthenticatePlayer, Args: secretPayload}},
					},
				},
			},
			{
				EventId: 3, EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED,
				Attributes: &historypb.HistoryEvent_WorkflowExecutionFailedEventAttributes{
					WorkflowExecutionFailedEventAttributes: &historypb.WorkflowExecutionFailedEventAttributes{
						Failure: &failurepb.Failure{Message: "pbkdf2-super-secret"},
					},
				},
			},
			{
				EventId: 4, EventType: enums.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
				Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{
					WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{
						TaskQueue: &taskqueuepb.TaskQueue{
							Name: "private-host.example:sticky-id", NormalName: "temporal-word-game",
							Kind: enums.TASK_QUEUE_KIND_STICKY,
						},
					},
				},
			},
		}}},
	}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "alice", nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/workflow?workflowId="+url.QueryEscape(workflowID), nil)
	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "pbkdf2-super-secret")
	require.NotContains(t, response.Body.String(), "SECRET")
	require.NotContains(t, response.Body.String(), "passwordHash")
	require.NotContains(t, response.Body.String(), "json/plain")
	require.NotContains(t, response.Body.String(), "private-host.example")
	require.Contains(t, response.Body.String(), "Safe title")
	require.Contains(t, response.Body.String(), `"words":"[redacted]"`)
	require.Contains(t, response.Body.String(), `"message":"[redacted]"`)
	require.Contains(t, response.Body.String(), `"details":`)
	require.Contains(t, response.Body.String(), `"normalName":"temporal-word-game"`)
	require.Contains(t, response.Body.String(), workflows.UpdateAuthenticatePlayer)
	require.Contains(t, response.Body.String(), `"title":"Workflow execution started"`)
	require.Contains(t, response.Body.String(), `"category":"lifecycle"`)
	require.Equal(t, runID, reader.historyRequest.GetExecution().GetRunId())
}

func TestChildWorkflowEventLinksToVisibleChildHistory(t *testing.T) {
	parentID := workflows.PlayerWorkflowID("alice")
	childID := workflows.WordflowLevelWorkflowID("alice", "campaign", 1)
	reader := &fakeReader{
		description: &workflowservicepb.DescribeWorkflowExecutionResponse{
			WorkflowExecutionInfo: executionInfo(parentID, workflows.PlayerWorkflowName),
		},
		historyResponse: &workflowservicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{
			Events: []*historypb.HistoryEvent{childWorkflowStartedEvent(childID, "child-run")},
		}},
	}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "alice", nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/workflow?workflowId="+url.QueryEscape(parentID), nil)

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var detail workflowDetailResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	require.Len(t, detail.Events, 1)
	require.Equal(t, &workflowReference{
		WorkflowID: childID,
		RunID:      "child-run",
		Type:       workflows.WordflowLevelWorkflowName,
		Kind:       "level",
	}, detail.Events[0].ChildWorkflow)
}

func TestPublicHistoryDoesNotLinkToPrivateChild(t *testing.T) {
	parentID := workflows.WordflowCampaignWorkflowID("public")
	childID := workflows.WordflowLevelWorkflowID("alice", "campaign", 1)
	reader := &fakeReader{
		description: &workflowservicepb.DescribeWorkflowExecutionResponse{
			WorkflowExecutionInfo: executionInfo(parentID, workflows.WordflowCampaignWorkflowName),
		},
		historyResponse: &workflowservicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{
			Events: []*historypb.HistoryEvent{childWorkflowStartedEvent(childID, "private-run")},
		}},
	}
	handler := newHandler(reader, func(*http.Request) (string, error) { return "", errors.New("not signed in") })
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/workflow?workflowId="+url.QueryEscape(parentID), nil)

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), `"childWorkflow"`)
	require.NotContains(t, response.Body.String(), childID)
}

func TestExplorerServesHardenedPage(t *testing.T) {
	handler := newHandler(&fakeReader{}, func(*http.Request) (string, error) {
		return "", errors.New("not signed in")
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "Workflow Explorer")
	require.Contains(t, response.Header().Get("Content-Security-Policy"), "default-src 'self'")
	require.Equal(t, "DENY", response.Header().Get("X-Frame-Options"))
}

func TestPublicEventDetailsRedactPlayerAndPuzzleData(t *testing.T) {
	payload := &commonpb.Payloads{Payloads: []*commonpb.Payload{{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data: []byte(`{
			"playerId":"alice",
			"username":"alice",
			"displayName":"Alice Display",
			"letters":"SECRET",
			"excludedWords":["HIDDEN"]
		}`),
	}}}
	event := &historypb.HistoryEvent{
		EventId: 1, EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionSignaledEventAttributes{
			WorkflowExecutionSignaledEventAttributes: &historypb.WorkflowExecutionSignaledEventAttributes{
				SignalName: "public-event", Input: payload,
			},
		},
	}

	encoded, err := json.Marshal(sanitizeEvent(event, true))
	require.NoError(t, err)
	body := string(encoded)
	require.Contains(t, body, `"playerId":"[redacted]"`)
	require.Contains(t, body, `"username":"[redacted]"`)
	require.Contains(t, body, `"displayName":"Alice Display"`)
	require.Contains(t, body, `"letters":"[redacted]"`)
	require.Contains(t, body, `"excludedWords":"[redacted]"`)
	require.NotContains(t, body, "SECRET")
	require.NotContains(t, body, "HIDDEN")
}

func TestPlayerIDPrefixDoesNotAuthorizeAnotherPlayer(t *testing.T) {
	require.True(t, canViewWorkflow("alice", workflows.WordflowLevelWorkflowID("alice", "campaign", 1), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED))
	require.False(t, canViewWorkflow("alice", workflows.WordflowLevelWorkflowID("alice2", "campaign", 1), enums.WORKFLOW_EXECUTION_STATUS_RUNNING))
	require.False(t, canViewWorkflow("alice", workflows.PlayerWorkflowID("alice2"), enums.WORKFLOW_EXECUTION_STATUS_RUNNING))
}

func executionInfo(workflowID, workflowType string) *workflowpb.WorkflowExecutionInfo {
	started := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	return &workflowpb.WorkflowExecutionInfo{
		Execution:            &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-id"},
		Type:                 &commonpb.WorkflowType{Name: workflowType},
		StartTime:            timestamppb.New(started),
		Status:               enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
		HistoryLength:        3,
		HistorySizeBytes:     128,
		StateTransitionCount: 2,
	}
}

func childWorkflowStartedEvent(workflowID, runID string) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId: 1, EventType: enums.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionStartedEventAttributes{
			ChildWorkflowExecutionStartedEventAttributes: &historypb.ChildWorkflowExecutionStartedEventAttributes{
				WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: runID},
				WorkflowType:      &commonpb.WorkflowType{Name: workflows.WordflowLevelWorkflowName},
			},
		},
	}
}
