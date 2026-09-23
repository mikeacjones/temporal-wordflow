const state = {
  workflows: [],
  nextWorkflowPage: "",
  selected: null,
  events: [],
  nextHistoryPage: "",
  detailRequest: 0,
  expandedEvents: new Set(),
  autoRefreshing: false,
  initialSelection: initialWorkflowSelection(),
};

const elements = Object.fromEntries([
  "viewer-badge", "workflow-list", "workflow-search", "refresh-list", "load-more-workflows",
  "empty-detail", "workflow-detail", "detail-kind", "detail-status", "detail-id", "detail-type",
  "refresh-detail", "metric-started", "metric-history", "metric-transitions", "metric-size",
  "version-strip", "version-behavior", "version-deployment", "version-build", "pending-strip",
  "show-worker-events", "timeline", "load-more-events",
].map((id) => [id, document.getElementById(id)]));

elements["workflow-search"].addEventListener("input", renderWorkflowList);
elements["show-worker-events"].addEventListener("change", renderTimeline);
elements["refresh-list"].addEventListener("click", () => loadWorkflows(false));
elements["load-more-workflows"].addEventListener("click", () => loadWorkflows(true));
elements["refresh-detail"].addEventListener("click", () => loadWorkflowDetail(state.selected, false));
elements["load-more-events"].addEventListener("click", () => loadWorkflowDetail(state.selected, true));

loadWorkflows(false);
setInterval(refreshVisibleData, 5_000);

async function loadWorkflows(append, quiet = false) {
  const button = append ? elements["load-more-workflows"] : elements["refresh-list"];
  if (!quiet) button.disabled = true;
  if (!append) {
    state.nextWorkflowPage = "";
    if (!quiet && !state.workflows.length) renderWorkflowLoading();
  }
  try {
    const suffix = append && state.nextWorkflowPage ? `?page=${encodeURIComponent(state.nextWorkflowPage)}` : "";
    const response = await fetchJSON(`api/workflows${suffix}`);
    state.workflows = append ? deduplicateWorkflows([...state.workflows, ...response.workflows]) : response.workflows;
    state.nextWorkflowPage = response.nextPageToken || "";
    elements["viewer-badge"].textContent = response.viewer.authenticated
      ? `Private view · ${response.viewer.playerId}`
      : "Public workflow view";
    elements["viewer-badge"].classList.remove("skeleton-text");
    renderWorkflowList();
    elements["load-more-workflows"].classList.toggle("hidden", !state.nextWorkflowPage);
    if (!state.selected) {
      const requested = state.initialSelection;
      state.initialSelection = null;
      const workflow = requested
        ? state.workflows.find((candidate) => candidate.workflowId === requested.workflowId
          && (!requested.runId || candidate.runId === requested.runId)) || requested
        : state.workflows[0];
      if (workflow) loadWorkflowDetail(workflow, false);
    }
  } catch (error) {
    if (!quiet) renderListError(error.message);
  } finally {
    if (!quiet) button.disabled = false;
  }
}

function renderWorkflowLoading() {
  elements["workflow-list"].replaceChildren(...Array.from({ length: 3 }, () => {
    const card = element("div", "loading-card");
    card.append(element("span"), element("span"), element("span"));
    return card;
  }));
}

function renderListError(message) {
  const error = element("div", "error-box", message);
  elements["workflow-list"].replaceChildren(error);
}

function renderWorkflowList() {
  const term = elements["workflow-search"].value.trim().toLowerCase();
  const visible = state.workflows.filter((workflow) =>
    !term || workflow.workflowId.toLowerCase().includes(term) || workflow.type.toLowerCase().includes(term)
      || workflow.kind.toLowerCase().includes(term));
  if (!visible.length) {
    elements["workflow-list"].replaceChildren(element("div", "empty-list",
      term ? "No workflows match that filter." : "No visible Wordflow workflows were found."));
    return;
  }
  elements["workflow-list"].replaceChildren(...visible.map(workflowCard));
}

function workflowCard(workflow) {
  const button = element("button", "workflow-card");
  button.type = "button";
  button.classList.toggle("active", sameWorkflow(workflow, state.selected));
  button.setAttribute("aria-pressed", sameWorkflow(workflow, state.selected) ? "true" : "false");

  const top = element("span", "workflow-card-top");
  top.append(element("span", "workflow-card-kind", workflow.kind), statusElement(workflow.status));
  const id = element("span", "workflow-card-id", workflow.workflowId);
  id.title = workflow.workflowId;
  const meta = element("span", "workflow-card-meta");
  meta.append(element("span", "", workflow.type), element("span", "", relativeTime(workflow.startedAt)));
  button.append(top, id, meta);
  button.addEventListener("click", () => loadWorkflowDetail(workflow, false));
  return button;
}

async function loadWorkflowDetail(workflow, append, quiet = false) {
  if (!workflow) return;
  const requestNumber = ++state.detailRequest;
  const button = append ? elements["load-more-events"] : elements["refresh-detail"];
  if (!quiet) button.disabled = true;
  if (!append) {
    if (!sameWorkflow(workflow, state.selected)) state.expandedEvents.clear();
    state.selected = workflow;
    state.nextHistoryPage = "";
    renderWorkflowList();
    if (!quiet) {
      state.events = [];
      renderDetailLoading(workflow);
    }
  }
  try {
    const params = new URLSearchParams({ workflowId: workflow.workflowId });
    if (workflow.runId) params.set("runId", workflow.runId);
    if (append && state.nextHistoryPage) params.set("historyPage", state.nextHistoryPage);
    const response = await fetchJSON(`api/workflow?${params}`);
    if (requestNumber !== state.detailRequest) return;
    state.selected = response.workflow;
    state.events = append ? [...state.events, ...response.events] : response.events;
    state.nextHistoryPage = response.nextPageToken || "";
    updateExplorerURL(response.workflow);
    renderDetail(response);
  } catch (error) {
    if (requestNumber !== state.detailRequest) return;
    if (!quiet) {
      elements["workflow-detail"].classList.remove("hidden");
      elements["empty-detail"].classList.add("hidden");
      elements.timeline.replaceChildren(element("li", "error-box", error.message));
    }
  } finally {
    if (!quiet) button.disabled = false;
  }
}

function renderDetailLoading(workflow) {
  elements["empty-detail"].classList.add("hidden");
  elements["workflow-detail"].classList.remove("hidden");
  elements["detail-kind"].textContent = workflow.kind;
  elements["detail-status"].textContent = "Loading";
  elements["detail-id"].textContent = workflow.workflowId;
  elements["detail-type"].textContent = workflow.type;
  elements.timeline.replaceChildren(element("li", "timeline-empty", "Loading sanitized history…"));
}

function renderDetail(response) {
  const workflow = response.workflow;
  elements["detail-kind"].textContent = `${workflow.visibility} · ${workflow.kind}`;
  elements["detail-status"].textContent = workflow.status.replaceAll("_", " ");
  elements["detail-id"].textContent = workflow.workflowId;
  elements["detail-type"].textContent = `${workflow.type} · Run ${shortID(workflow.runId)}`;
  elements["metric-started"].textContent = formatDate(workflow.startedAt);
  elements["metric-history"].textContent = number(workflow.historyLength);
  elements["metric-transitions"].textContent = number(workflow.stateTransitionCount);
  elements["metric-size"].textContent = formatBytes(workflow.historySizeBytes);

  const hasVersion = workflow.versioningBehavior || workflow.workerDeployment || workflow.workerBuildId;
  elements["version-strip"].classList.toggle("hidden", !hasVersion);
  elements["version-behavior"].textContent = workflow.versioningBehavior || "unversioned";
  elements["version-deployment"].textContent = workflow.workerDeployment || "—";
  elements["version-build"].textContent = workflow.workerBuildId || "—";

  const pending = [];
  if (response.pending.activities) pending.push(`${response.pending.activities} pending ${plural(response.pending.activities, "Activity", "Activities")}`);
  if (response.pending.children) pending.push(`${response.pending.children} pending child ${plural(response.pending.children, "Workflow", "Workflows")}`);
  if (response.pending.nexus) pending.push(`${response.pending.nexus} pending Nexus ${plural(response.pending.nexus, "operation", "operations")}`);
  elements["pending-strip"].textContent = pending.join(" · ");
  elements["pending-strip"].classList.toggle("hidden", !pending.length);
  elements["load-more-events"].classList.toggle("hidden", !state.nextHistoryPage);
  renderTimeline();
}

function renderTimeline() {
  const showWorker = elements["show-worker-events"].checked;
  const events = state.events.filter((event) => showWorker || event.category !== "worker");
  if (!events.length) {
    elements.timeline.replaceChildren(element("li", "timeline-empty",
      state.events.length ? "Only Workflow Task events are on this page. Enable the toggle to see them." : "No history events were returned."));
    return;
  }
  elements.timeline.replaceChildren(...events.map((event) => {
    const item = element("li", `timeline-event ${event.category}`);
    const disclosure = document.createElement("details");
    disclosure.className = "event-disclosure";
    disclosure.open = state.expandedEvents.has(event.id);
    disclosure.addEventListener("toggle", () => {
      if (disclosure.open) state.expandedEvents.add(event.id);
      else state.expandedEvents.delete(event.id);
    });
    const summary = document.createElement("summary");
    const number = element("span", "event-number", String(event.id));
    const copy = element("span", "event-copy");
    copy.append(element("strong", "", event.title));
    if (event.detail) copy.append(element("code", "", event.detail));
    summary.append(number, copy, element("time", "event-time", formatTime(event.time)));
    const payload = element("pre", "event-payload");
    payload.textContent = JSON.stringify(event.details || {}, null, 2);
    disclosure.append(summary);
    if (event.childWorkflow) {
      const childLink = element("a", "child-workflow-link", "Open child history →");
      childLink.href = workflowHref(event.childWorkflow);
      childLink.title = event.childWorkflow.workflowId;
      childLink.addEventListener("click", (click) => {
        if (click.button !== 0 || click.metaKey || click.ctrlKey || click.shiftKey || click.altKey) return;
        click.preventDefault();
        loadWorkflowDetail(event.childWorkflow, false);
      });
      disclosure.append(childLink);
    }
    disclosure.append(payload);
    item.append(disclosure);
    return item;
  }));
}

async function refreshVisibleData() {
  if (document.hidden || state.autoRefreshing) return;
  state.autoRefreshing = true;
  try {
    const selected = state.selected;
    await Promise.all([
      loadWorkflows(false, true),
      selected ? loadWorkflowDetail(selected, false, true) : Promise.resolve(),
    ]);
  } finally {
    state.autoRefreshing = false;
  }
}

async function fetchJSON(url) {
  const response = await fetch(url, { credentials: "same-origin", headers: { Accept: "application/json" } });
  let body;
  try { body = await response.json(); } catch { body = {}; }
  if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
  return body;
}

function element(tag, className = "", text = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== "") node.textContent = text;
  return node;
}

function statusElement(status) {
  return element("span", `status ${status}`, status.replaceAll("_", " "));
}

function deduplicateWorkflows(workflows) {
  const seen = new Set();
  return workflows.filter((workflow) => {
    const key = `${workflow.workflowId}/${workflow.runId}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function sameWorkflow(left, right) {
  return Boolean(left && right && left.workflowId === right.workflowId && left.runId === right.runId);
}

function initialWorkflowSelection() {
  const params = new URLSearchParams(window.location.search);
  const workflowId = params.get("workflowId");
  if (!workflowId) return null;
  return { workflowId, runId: params.get("runId") || "", type: "Workflow", kind: "workflow" };
}

function updateExplorerURL(workflow) {
  const url = new URL(window.location.href);
  url.search = new URLSearchParams({ workflowId: workflow.workflowId, runId: workflow.runId }).toString();
  window.history.replaceState(null, "", url);
}

function workflowHref(workflow) {
  const params = new URLSearchParams({ workflowId: workflow.workflowId });
  if (workflow.runId) params.set("runId", workflow.runId);
  return `?${params}`;
}

function shortID(value) { return value ? `${value.slice(0, 8)}…` : "latest"; }
function number(value) { return new Intl.NumberFormat().format(value || 0); }
function plural(count, singular, pluralValue) { return count === 1 ? singular : pluralValue; }

function formatDate(value) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(new Date(value));
}

function formatTime(value) {
  if (!value) return "";
  return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit", second: "2-digit" }).format(new Date(value));
}

function relativeTime(value) {
  if (!value) return "";
  const elapsed = Date.now() - Date.parse(value);
  if (elapsed < 60_000) return "now";
  if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)}m ago`;
  if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)}h ago`;
  return `${Math.floor(elapsed / 86_400_000)}d ago`;
}

function formatBytes(value) {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`;
}
