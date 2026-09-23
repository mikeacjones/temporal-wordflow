package explorer

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	maxDetailDepth  = 16
	maxDetailFields = 60
	maxDetailItems  = 30
	maxDetailString = 1000
)

var detailConverter = converter.GetDefaultDataConverter()

type childWorkflowEventAttributes interface {
	GetWorkflowExecution() *commonpb.WorkflowExecution
	GetWorkflowType() *commonpb.WorkflowType
}

func sanitizeEvent(event *historypb.HistoryEvent, public bool) eventView {
	eventType := cleanEnum(event.GetEventType().String(), "EVENT_TYPE_")
	details := map[string]any{
		"eventId":   event.GetEventId(),
		"eventType": eventType,
	}
	if eventTime := timestamp(event.GetEventTime()); eventTime != nil {
		details["occurredAt"] = eventTime
	}
	if event.GetVersion() != 0 {
		details["eventVersion"] = event.GetVersion()
	}

	var detail string
	if attributes := eventAttributes(event); attributes.IsValid() {
		for key, value := range sanitizeProtoMessage(attributes, public, 0) {
			details[key] = value
		}
		detail = eventDetail(attributes)
	}

	return eventView{
		ID:       event.GetEventId(),
		Time:     timestamp(event.GetEventTime()),
		Type:     eventType,
		Category: eventCategory(eventType),
		Title:    humanize(eventType),
		Detail:   detail,
		Details:  details,
	}
}

func childWorkflowReference(event *historypb.HistoryEvent) (*workflowReference, enums.WorkflowExecutionStatus) {
	var attributes childWorkflowEventAttributes
	status := enums.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED
	switch {
	case event.GetChildWorkflowExecutionStartedEventAttributes() != nil:
		attributes = event.GetChildWorkflowExecutionStartedEventAttributes()
		status = enums.WORKFLOW_EXECUTION_STATUS_RUNNING
	case event.GetChildWorkflowExecutionCompletedEventAttributes() != nil:
		attributes = event.GetChildWorkflowExecutionCompletedEventAttributes()
		status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	case event.GetChildWorkflowExecutionFailedEventAttributes() != nil:
		attributes = event.GetChildWorkflowExecutionFailedEventAttributes()
		status = enums.WORKFLOW_EXECUTION_STATUS_FAILED
	case event.GetChildWorkflowExecutionCanceledEventAttributes() != nil:
		attributes = event.GetChildWorkflowExecutionCanceledEventAttributes()
		status = enums.WORKFLOW_EXECUTION_STATUS_CANCELED
	case event.GetChildWorkflowExecutionTimedOutEventAttributes() != nil:
		attributes = event.GetChildWorkflowExecutionTimedOutEventAttributes()
		status = enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT
	case event.GetChildWorkflowExecutionTerminatedEventAttributes() != nil:
		attributes = event.GetChildWorkflowExecutionTerminatedEventAttributes()
		status = enums.WORKFLOW_EXECUTION_STATUS_TERMINATED
	default:
		return nil, status
	}

	execution := attributes.GetWorkflowExecution()
	if execution.GetWorkflowId() == "" {
		return nil, status
	}
	return &workflowReference{
		WorkflowID: execution.GetWorkflowId(),
		RunID:      execution.GetRunId(),
		Type:       safeWorkflowType(attributes.GetWorkflowType().GetName()),
		Kind:       workflowKind(execution.GetWorkflowId()),
	}, status
}

func eventAttributes(event *historypb.HistoryEvent) protoreflect.Message {
	message := event.ProtoReflect()
	fields := message.Descriptor().Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		if strings.HasSuffix(string(field.Name()), "_event_attributes") && message.Has(field) {
			return message.Get(field).Message()
		}
	}
	return nil
}

func eventDetail(attributes protoreflect.Message) string {
	if value := findStringField(attributes, map[string]bool{
		"activity_type": true,
		"marker_name":   true,
		"signal_name":   true,
		"timer_id":      true,
	}); value != "" {
		return value
	}
	if value := findStringField(attributes, map[string]bool{"name": true}); value != "" {
		if operation := safeOperationName(value); operation != "" {
			return operation
		}
		if workflowType := safeWorkflowType(value); workflowType != "Workflow" {
			return workflowType
		}
	}
	return ""
}

func findStringField(message protoreflect.Message, names map[string]bool) string {
	var found string
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if names[string(field.Name())] && field.Kind() == protoreflect.StringKind {
			found = value.String()
			return false
		}
		if field.Kind() == protoreflect.MessageKind && !field.IsMap() && !field.IsList() && message.Has(field) {
			found = findStringField(value.Message(), names)
			return found == ""
		}
		return true
	})
	return found
}

func sanitizeProtoMessage(message protoreflect.Message, public bool, depth int) map[string]any {
	if !message.IsValid() || depth >= maxDetailDepth {
		return map[string]any{"value": "[truncated]"}
	}
	if payload, ok := message.Interface().(*commonpb.Payload); ok {
		return map[string]any{"value": sanitizePayload(payload, public, depth+1)}
	}
	if payloads, ok := message.Interface().(*commonpb.Payloads); ok {
		return map[string]any{"value": sanitizePayloads(payloads, public, depth+1)}
	}
	if failure, ok := message.Interface().(*failurepb.Failure); ok {
		return sanitizeFailure(failure)
	}

	result := make(map[string]any)
	fields := make([]protoreflect.FieldDescriptor, 0)
	message.Range(func(field protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		fields = append(fields, field)
		return true
	})
	sort.Slice(fields, func(left, right int) bool { return fields[left].JSONName() < fields[right].JSONName() })
	for index, field := range fields {
		if index >= maxDetailFields {
			result["truncated"] = true
			break
		}
		name := field.JSONName()
		if omitDetailField(name) {
			continue
		}
		value := sanitizeProtoValue(field, message.Get(field), public, depth+1)
		if value != nil {
			result[name] = value
		}
	}
	return result
}

func sanitizeProtoValue(field protoreflect.FieldDescriptor, value protoreflect.Value, public bool, depth int) any {
	if field.IsList() {
		list := value.List()
		limit := min(list.Len(), maxDetailItems)
		items := make([]any, 0, limit+1)
		for index := 0; index < limit; index++ {
			items = append(items, sanitizeSingularValue(field, list.Get(index), public, depth))
		}
		if list.Len() > limit {
			items = append(items, fmt.Sprintf("[%d more items]", list.Len()-limit))
		}
		return items
	}
	if field.IsMap() {
		mapped := value.Map()
		keys := make([]string, 0, mapped.Len())
		mapped.Range(func(key protoreflect.MapKey, _ protoreflect.Value) bool {
			keys = append(keys, key.String())
			return true
		})
		sort.Strings(keys)
		result := make(map[string]any)
		for index, key := range keys {
			if index >= maxDetailFields {
				result["truncated"] = true
				break
			}
			if omitDetailField(key) {
				continue
			}
			mapKey := protoreflect.ValueOfString(key).MapKey()
			result[key] = sanitizeMapValue(field.MapValue(), mapped.Get(mapKey), public, depth)
		}
		return result
	}
	return sanitizeSingularValue(field, value, public, depth)
}

func sanitizeMapValue(field protoreflect.FieldDescriptor, value protoreflect.Value, public bool, depth int) any {
	if field.Kind() == protoreflect.MessageKind {
		return unwrapSanitizedMessage(value.Message(), public, depth)
	}
	return sanitizeScalar(field, value, public)
}

func sanitizeSingularValue(field protoreflect.FieldDescriptor, value protoreflect.Value, public bool, depth int) any {
	if field.Kind() == protoreflect.MessageKind {
		return unwrapSanitizedMessage(value.Message(), public, depth)
	}
	return sanitizeScalar(field, value, public)
}

func unwrapSanitizedMessage(message protoreflect.Message, public bool, depth int) any {
	if payload, ok := message.Interface().(*commonpb.Payload); ok {
		return sanitizePayload(payload, public, depth+1)
	}
	if payloads, ok := message.Interface().(*commonpb.Payloads); ok {
		return sanitizePayloads(payloads, public, depth+1)
	}
	if failure, ok := message.Interface().(*failurepb.Failure); ok {
		return sanitizeFailure(failure)
	}
	if taskQueue, ok := message.Interface().(*taskqueuepb.TaskQueue); ok {
		return sanitizeTaskQueue(taskQueue)
	}
	return sanitizeProtoMessage(message, public, depth+1)
}

func sanitizeTaskQueue(taskQueue *taskqueuepb.TaskQueue) map[string]any {
	result := map[string]any{
		"kind": cleanEnum(taskQueue.GetKind().String(), "TASK_QUEUE_KIND_"),
	}
	if taskQueue.GetKind() == enums.TASK_QUEUE_KIND_STICKY {
		result["name"] = "[redacted]"
	} else {
		result["name"] = truncateString(taskQueue.GetName())
	}
	if taskQueue.GetNormalName() != "" {
		result["normalName"] = truncateString(taskQueue.GetNormalName())
	}
	return result
}

func sanitizeScalar(field protoreflect.FieldDescriptor, value protoreflect.Value, public bool) any {
	switch field.Kind() {
	case protoreflect.StringKind:
		text := truncateString(value.String())
		if public && redactPublicIdentifier(field.JSONName(), text) {
			return "[redacted]"
		}
		return text
	case protoreflect.BoolKind:
		return value.Bool()
	case protoreflect.EnumKind:
		enumValue := field.Enum().Values().ByNumber(value.Enum())
		if enumValue == nil {
			return value.Enum()
		}
		return cleanEnum(string(enumValue.Name()), "")
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return value.Int()
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return value.Uint()
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return value.Float()
	case protoreflect.BytesKind:
		return map[string]any{"sizeBytes": len(value.Bytes())}
	default:
		return nil
	}
}

func sanitizePayloads(payloads *commonpb.Payloads, public bool, depth int) any {
	if payloads == nil || len(payloads.GetPayloads()) == 0 {
		return nil
	}
	values := make([]any, 0, min(len(payloads.GetPayloads()), maxDetailItems))
	for index, payload := range payloads.GetPayloads() {
		if index >= maxDetailItems {
			values = append(values, fmt.Sprintf("[%d more payloads]", len(payloads.GetPayloads())-index))
			break
		}
		values = append(values, sanitizePayload(payload, public, depth+1))
	}
	if len(values) == 1 {
		return values[0]
	}
	return values
}

func sanitizePayload(payload *commonpb.Payload, public bool, depth int) any {
	if payload == nil {
		return nil
	}
	var decoded any
	if err := detailConverter.FromPayload(payload, &decoded); err != nil {
		return map[string]any{"sizeBytes": len(payload.GetData())}
	}
	return sanitizeDecoded(decoded, public, depth+1)
}

func sanitizeDecoded(value any, public bool, depth int) any {
	if depth >= maxDetailDepth {
		return "[truncated]"
	}
	switch typed := value.(type) {
	case nil, bool, float64:
		return typed
	case string:
		return truncateString(typed)
	case []any:
		limit := min(len(typed), maxDetailItems)
		result := make([]any, 0, limit+1)
		for index := 0; index < limit; index++ {
			result = append(result, sanitizeDecoded(typed[index], public, depth+1))
		}
		if len(typed) > limit {
			result = append(result, fmt.Sprintf("[%d more items]", len(typed)-limit))
		}
		return result
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make(map[string]any)
		for index, key := range keys {
			if index >= maxDetailFields {
				result["truncated"] = true
				break
			}
			if omitDetailField(key) {
				continue
			}
			if redactPuzzleField(key) {
				result[key] = "[redacted]"
				continue
			}
			if public && redactPublicIdentifier(key, fmt.Sprint(typed[key])) {
				result[key] = "[redacted]"
				continue
			}
			result[key] = sanitizeDecoded(typed[key], public, depth+1)
		}
		return result
	default:
		return truncateString(fmt.Sprint(typed))
	}
}

func sanitizeFailure(failure *failurepb.Failure) map[string]any {
	result := map[string]any{"message": "[redacted]"}
	switch {
	case failure.GetApplicationFailureInfo() != nil:
		result["kind"] = "application"
		result["type"] = truncateString(failure.GetApplicationFailureInfo().GetType())
		result["nonRetryable"] = failure.GetApplicationFailureInfo().GetNonRetryable()
	case failure.GetTimeoutFailureInfo() != nil:
		result["kind"] = "timeout"
		result["timeoutType"] = cleanEnum(failure.GetTimeoutFailureInfo().GetTimeoutType().String(), "TIMEOUT_TYPE_")
	case failure.GetCanceledFailureInfo() != nil:
		result["kind"] = "canceled"
	case failure.GetTerminatedFailureInfo() != nil:
		result["kind"] = "terminated"
	case failure.GetActivityFailureInfo() != nil:
		result["kind"] = "activity"
		result["activityType"] = truncateString(failure.GetActivityFailureInfo().GetActivityType().GetName())
		result["retryState"] = cleanEnum(failure.GetActivityFailureInfo().GetRetryState().String(), "RETRY_STATE_")
	case failure.GetChildWorkflowExecutionFailureInfo() != nil:
		result["kind"] = "child_workflow"
		result["workflowType"] = safeWorkflowType(failure.GetChildWorkflowExecutionFailureInfo().GetWorkflowType().GetName())
		result["retryState"] = cleanEnum(failure.GetChildWorkflowExecutionFailureInfo().GetRetryState().String(), "RETRY_STATE_")
	default:
		result["kind"] = "failure"
	}
	return result
}

func omitDetailField(name string) bool {
	switch normalizeDetailKey(name) {
	case "password", "passwordhash", "secret", "token", "apikey", "authorization", "cookie",
		"credential", "credentials", "sessionjwtsecret", "header", "headers", "memo", "searchattributes",
		"identity", "requestid", "tasktoken", "stacktrace", "encodedattributes":
		return true
	default:
		return false
	}
}

func redactPuzzleField(name string) bool {
	switch normalizeDetailKey(name) {
	case "answer", "answers", "excludedwords", "solution", "solutions", "solutionwords", "word", "words", "letters", "puzzle", "puzzles":
		return true
	default:
		return false
	}
}

func redactPublicIdentifier(name, value string) bool {
	normalized := normalizeDetailKey(name)
	return normalized == "playerid" || normalized == "username" ||
		(normalized == "workflowid" && (strings.HasPrefix(value, "player/") || strings.HasPrefix(value, "wordflow-level/")))
}

func normalizeDetailKey(value string) string {
	var normalized strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			normalized.WriteRune(unicode.ToLower(character))
		}
	}
	return normalized.String()
}

func truncateString(value string) string {
	if utf8.RuneCountInString(value) <= maxDetailString {
		return value
	}
	return string([]rune(value)[:maxDetailString]) + "…"
}
