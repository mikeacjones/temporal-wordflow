let player;
let catalog;
let currentGame;
let selectedIndexes = [];
let completionRefresh;
let completionAction;
let noticeTimer;
let speedBonusTimer;
let campaignTimer;
const refreshedCampaignBoundaries = new Set();
let levelDeadlineTimer;
let deadlineRefresh;

const elements = Object.fromEntries(
  [...document.querySelectorAll("[id]")].map((element) => [element.id, element]),
);

function requestID() {
  return crypto.randomUUID();
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const body = await response.json();
  if (!response.ok) {
    const error = new Error(body.error || "Request failed");
    error.status = response.status;
    throw error;
  }
  return body;
}

async function openSession() {
  let session;
  try {
    session = await api("/api/session");
  } catch (error) {
    if (error.status === 401) {
      showAuthentication();
      return;
    }
    throw error;
  }
  player = session.player;
  await showPlayer(session.catalog, session.game);
}

async function authenticate(path, form) {
  const input = Object.fromEntries(new FormData(form));
  input.requestId = requestID();
  showCampaignLoadingScreen();
  let session;
  try {
    session = await api(path, { method: "POST", body: JSON.stringify(input) });
  } catch (error) {
    showAuthentication();
    throw error;
  }
  player = session.player;
  form.reset();
  await showPlayer(session.catalog, session.game);
}

async function logOut() {
  await api("/api/session", { method: "DELETE" });
  player = undefined;
  catalog = undefined;
  currentGame = undefined;
  showAuthentication();
}

function showAuthentication() {
  stopSpeedBonusTicker();
  stopCampaignTicker();
  stopLevelDeadlineTicker();
  document.body.dataset.session = "anonymous";
  elements.startup.hidden = true;
  elements.auth.hidden = false;
  elements.stats.hidden = true;
  elements["point-shop"].hidden = true;
  elements.catalog.hidden = true;
  elements.game.hidden = true;
  elements.complete.hidden = true;
  elements["player-name"].hidden = true;
  elements["player-workflow"].hidden = true;
  elements.logout.hidden = true;
}

function showCampaignLoadingScreen() {
  stopLevelDeadlineTicker();
  elements.startup.hidden = true;
  elements.auth.hidden = true;
  elements.stats.hidden = true;
  elements["point-shop"].hidden = true;
  elements.game.hidden = true;
  elements.complete.hidden = true;
  elements.catalog.hidden = false;
  renderCampaignLoading();
}

async function showPlayer(initialCatalog, initialGame) {
  document.body.dataset.session = "authenticated";
  elements.startup.hidden = true;
  elements.auth.hidden = true;
  elements["player-name"].hidden = false;
  elements["player-name"].textContent = player.displayName;
  elements["player-workflow"].hidden = false;
  elements.logout.hidden = false;
  renderPlayer();
  loadWorkflowLink(elements["player-workflow"], "/api/me/workflow-link").catch(showError);
  if (player.activeGame) {
    await resumeGame(initialGame);
  } else {
    currentGame = undefined;
    await showCatalog(initialCatalog);
  }
}

function renderPlayer() {
  elements.stats.innerHTML = [
    [player.currentStreak, "current streak"],
    [player.bestStreak, "best streak"],
    [player.points, "points"],
    [player.lifetimePointsEarned || 0, "lifetime earned"],
    [player.completedLevelCount || 0, "levels completed"],
  ].map(([value, label]) => `<div class="stat"><strong>${value}</strong><span>${label}</span></div>`).join("");

  elements["streak-freeze-status"].textContent = player.streakFreeze
    ? "Streak freeze ready"
    : "Streak freeze";
  elements["buy-streak-freeze"].textContent = player.streakFreeze
    ? "Purchased"
    : `Buy · ${player.streakFreezeCost} points`;
  elements["buy-streak-freeze"].disabled = player.streakFreeze || player.points < player.streakFreezeCost;
}

async function showCatalog(initialCatalog) {
  stopSpeedBonusTicker();
  stopCampaignTicker();
  stopLevelDeadlineTicker();
  currentGame = undefined;
  elements.stats.hidden = false;
  elements["point-shop"].hidden = false;
  elements.game.hidden = true;
  elements.complete.hidden = true;
  elements.catalog.hidden = false;
  if (!initialCatalog) renderCampaignLoading();
  catalog = initialCatalog || await api("/api/catalog");
  renderCatalog();
}

function renderCampaignLoading() {
  elements.campaigns.innerHTML = `
    <div class="campaign-loading" role="status" aria-live="polite">
      <span class="campaign-loading-mark" aria-hidden="true"><i></i><i></i><i></i></span>
      <strong>Loading campaigns</strong>
      <small>Reading durable state…</small>
    </div>`;
}

function renderCatalog() {
  const daily = currentDailyChallenge(catalog.campaigns);
  elements["daily-challenge-slot"].hidden = !daily;
  elements["daily-challenge-slot"].replaceChildren(...(daily ? [renderDailyChallenge(daily)] : []));
  elements.campaigns.replaceChildren(...catalog.campaigns
    .filter((campaign) => campaign.kind !== "daily-challenge")
    .map(renderCampaign));
  startCampaignTicker();
}

function currentDailyChallenge(campaigns) {
  const daily = campaigns.filter((campaign) => campaign.kind === "daily-challenge");
  return daily.find((campaign) => campaign.status === "active")
    || daily.filter((campaign) => campaign.status === "upcoming")
      .sort((left, right) => Date.parse(left.startsAt) - Date.parse(right.startsAt))[0];
}

function renderDailyChallenge(campaign) {
  const terminal = campaign.failed || campaign.completedLevels === campaign.totalLevels;
  const strip = document.createElement("section");
  strip.className = `daily-strip${terminal ? " terminal" : ""}${campaign.failed ? " failed" : ""}`;

  const mark = document.createElement("div");
  mark.className = "daily-mark";
  mark.setAttribute("aria-hidden", "true");
  mark.textContent = terminal ? (campaign.failed ? "×" : "✓") : "✦";

  const copy = document.createElement("div");
  copy.className = "daily-copy";
  const eyebrow = document.createElement("p");
  eyebrow.className = "eyebrow";
  eyebrow.textContent = terminal
    ? (campaign.failed ? "Daily challenge ended" : "Daily challenge complete")
    : "Daily Challenge";
  const title = document.createElement("h3");
  title.textContent = terminal
    ? `${campaign.completedLevels} / ${campaign.totalLevels} levels completed`
    : campaign.title;
  const detail = document.createElement("p");
  detail.className = "daily-detail";
  if (terminal) {
    detail.append("Returns in ", campaignCountdown(campaign.endsAt));
  } else if (campaign.status === "upcoming") {
    detail.append(`${campaign.totalLevels} expert levels · Starts in `, campaignCountdown(campaign.startsAt));
  } else {
    detail.append(`${campaign.totalLevels} expert levels · Hard time limits · `, campaignCountdown(campaign.endsAt), " left");
  }
  copy.append(eyebrow, title, detail);

  const progress = document.createElement("div");
  progress.className = "daily-progress";
  progress.setAttribute("aria-label", `${campaign.completedLevels} of ${campaign.totalLevels} daily levels completed`);
  progress.replaceChildren(...campaign.levels.map((level) => {
    const node = document.createElement("span");
    const displayStatus = levelDisplayStatus(campaign, level);
    node.className = displayStatus;
    node.textContent = level.level;
    node.title = `Level ${level.level}: ${level.title} · ${displayStatus}`;
    return node;
  }));

  const actions = document.createElement("div");
  actions.className = "daily-actions";
  const workflow = document.createElement("a");
  workflow.className = "quiet workflow-link compact";
  workflow.target = "_blank";
  workflow.rel = "noopener";
  workflow.textContent = "Workflow ↗";
  workflow.href = campaign.workflowUrl;

  const action = document.createElement("button");
  const activeLevel = campaign.levels.find((level) => level.status === "active");
  const availableLevel = campaign.levels.find((level) => level.status === "available");
  if (terminal) {
    action.className = "quiet";
    action.textContent = campaign.failed ? "View solution" : "View result";
    action.addEventListener("click", () => reviewDailyOutcome(campaign).catch(showError));
  } else if (activeLevel) {
    action.className = "primary";
    action.textContent = `Resume level ${activeLevel.level}`;
    action.addEventListener("click", () => resumeGame().catch(showError));
  } else if (availableLevel) {
    action.className = "primary";
    action.textContent = campaign.completedLevels ? "Continue challenge" : "Play Daily Challenge!";
    action.addEventListener("click", () => startGame(campaign.campaignId, availableLevel.level).catch(showError));
  } else {
    action.className = "quiet";
    action.textContent = campaign.lockedReason || "Challenge unavailable";
    action.disabled = true;
  }
  actions.append(workflow, action);
  strip.append(mark, copy, progress, actions);
  return strip;
}

function renderCampaign(campaign) {
  const card = document.createElement("section");
  card.className = "campaign-card";

  const heading = document.createElement("div");
  heading.className = "campaign-heading";
  const copy = document.createElement("div");
  const status = document.createElement("p");
  status.className = "eyebrow";
  status.textContent = campaign.status;
  const title = document.createElement("h3");
  title.textContent = campaign.title;
  copy.append(status, title);
  if (campaign.endsAt) {
    const availability = document.createElement("p");
    availability.className = "campaign-availability";
    availability.title = `Ends ${new Date(campaign.endsAt).toLocaleString()}`;
    if (campaign.status === "upcoming" && campaign.startsAt) {
      availability.append("Starts in ", campaignCountdown(campaign.startsAt));
    } else if (campaign.status === "active") {
      availability.append("Available for ", campaignCountdown(campaign.endsAt));
    } else {
      availability.textContent = "Campaign ended";
    }
    copy.append(availability);
  }

  const workflow = document.createElement("a");
  workflow.className = "quiet workflow-link compact";
  workflow.target = "_blank";
  workflow.rel = "noopener";
  workflow.textContent = "Campaign Workflow ↗";
  workflow.href = campaign.workflowUrl;
  heading.append(copy, workflow);

  const description = document.createElement("p");
  description.className = "campaign-description";
  description.textContent = campaign.description;
  const progress = document.createElement("p");
  progress.className = "campaign-progress";
  progress.textContent = campaign.failed
    ? `Attempt ended · ${campaign.completedLevels} / ${campaign.totalLevels} levels completed`
    : `${campaign.completedLevels} / ${campaign.totalLevels} levels completed`;

  const levels = document.createElement("div");
  levels.className = "campaign-levels";
  levels.replaceChildren(...campaign.levels.map((level) => {
    const item = document.createElement("span");
    const displayStatus = levelDisplayStatus(campaign, level);
    item.className = `campaign-level ${displayStatus}`;
    item.title = `Level ${level.level}: ${level.title} · ${displayStatus}`;
    item.dataset.level = level.level;
    if (level.status === "locked" && displayStatus === "locked" && level.unlockedAt) {
      item.dataset.locksUntil = level.unlockedAt;
    }
    item.textContent = level.level;
    return item;
  }));

  const activeLevel = campaign.levels.find((level) => level.status === "active");
  const availableLevel = campaign.levels.find((level) => level.status === "available");
  const nextLevel = campaign.levels.find((level) => level.level === campaign.nextLevel);
  const action = document.createElement("button");
  action.className = activeLevel || availableLevel ? "primary" : "quiet";
  if (activeLevel) {
    action.textContent = `Resume level ${activeLevel.level}`;
    action.addEventListener("click", () => resumeGame().catch(showError));
  } else if (availableLevel) {
    action.textContent = `${campaign.completedLevels ? "Continue" : "Start"} campaign`;
    action.addEventListener("click", () => startGame(campaign.campaignId, availableLevel.level).catch(showError));
  } else if (campaign.completedLevels === campaign.totalLevels) {
    action.textContent = "Campaign complete";
    action.disabled = true;
  } else if (!campaign.lockedReason && nextLevel?.unlockedAt) {
    const countdown = document.createElement("span");
    countdown.className = "campaign-unlock-countdown";
    action.classList.add("campaign-unlock");
    action.append(`Level ${nextLevel.level} unlocks in `, countdown);
    action.dataset.unlocksAt = nextLevel.unlockedAt;
    action.dataset.level = nextLevel.level;
    action.title = new Date(nextLevel.unlockedAt).toLocaleString();
    action.addEventListener("click", () => startGame(campaign.campaignId, nextLevel.level).catch(showError));
    action.disabled = true;
  } else {
    action.textContent = campaign.lockedReason || "Next level locked";
    action.disabled = true;
  }

  card.append(heading, description, progress, levels, action);
  return card;
}

function levelDisplayStatus(campaign, level, now = Date.now()) {
  if (campaign.lockedReason || level.status !== "locked" || !level.unlockedAt) return level.status;
  return Date.parse(level.unlockedAt) <= now ? "unlocked" : "locked";
}

function campaignCountdown(at) {
  const countdown = document.createElement("span");
  countdown.className = "campaign-countdown";
  countdown.dataset.countdownAt = at;
  return countdown;
}

function startCampaignTicker() {
  stopCampaignTicker();
  const lockedLevels = [...elements.catalog.querySelectorAll("[data-locks-until]")];
  const buttons = [...elements.catalog.querySelectorAll("[data-unlocks-at]")];
  const countdowns = [...elements.catalog.querySelectorAll("[data-countdown-at]")];
  const tick = () => {
    let waiting = false;
    lockedLevels.forEach((level) => {
      if (Date.parse(level.dataset.locksUntil) <= Date.now()) {
        level.classList.remove("locked");
        level.classList.add("unlocked");
        level.title = level.title.replace(/ · [^·]+$/, " · unlocked");
        delete level.dataset.locksUntil;
        return;
      }
      waiting = true;
    });
    buttons.forEach((button) => {
      const remaining = Date.parse(button.dataset.unlocksAt) - Date.now();
      const countdown = button.querySelector(".campaign-unlock-countdown");
      if (remaining <= 0) {
        button.disabled = false;
        button.classList.remove("quiet");
        button.classList.add("primary");
        button.textContent = `Start level ${button.dataset.level}`;
        const level = button.closest(".campaign-card")?.querySelector(`[data-level="${button.dataset.level}"]`);
        if (level) {
          level.classList.remove("locked", "unlocked");
          level.classList.add("available");
          level.title = level.title.replace(/ · [^·]+$/, " · available");
        }
        return;
      }
      countdown.textContent = formatUnlockCountdown(remaining);
      waiting = true;
    });
    for (const countdown of countdowns) {
      const remaining = Date.parse(countdown.dataset.countdownAt) - Date.now();
      if (remaining <= 0) {
        const boundary = countdown.dataset.countdownAt;
        countdown.textContent = "now";
        if (refreshedCampaignBoundaries.has(boundary)) continue;
        refreshedCampaignBoundaries.add(boundary);
        stopCampaignTicker();
        showCatalog().catch(showError);
        return false;
      }
      countdown.textContent = formatCampaignCountdown(remaining);
      waiting = true;
    }
    return waiting;
  };
  if (tick()) {
    campaignTimer = setInterval(() => {
      if (!tick()) stopCampaignTicker();
    }, 1000);
  }
}

function stopCampaignTicker() {
  clearInterval(campaignTimer);
  campaignTimer = undefined;
}

function formatUnlockCountdown(milliseconds) {
  const totalSeconds = Math.max(0, Math.ceil(milliseconds / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  return [hours, minutes, seconds].map((value) => String(value).padStart(2, "0")).join(":");
}

function formatCampaignCountdown(milliseconds) {
  const totalSeconds = Math.max(0, Math.ceil(milliseconds / 1000));
  const days = Math.floor(totalSeconds / 86400);
  const hours = Math.floor((totalSeconds % 86400) / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  return `${String(days).padStart(2, "0")}d ${[hours, minutes, seconds]
    .map((value) => String(value).padStart(2, "0"))
    .join(":")}`;
}

async function startGame(campaignId, level) {
  player = await api(`/api/me/campaigns/${campaignId}/levels`, {
    method: "POST",
    body: JSON.stringify({ requestId: requestID(), level }),
  });
  await resumeGame();
  renderPlayer();
}

async function resumeGame(initialGame) {
  const active = player.activeGame;
  currentGame = initialGame || await api(`/api/me/campaigns/${active.campaignId}/levels/${active.level}`);
  completionRefresh = undefined;
  completionAction = undefined;
  deadlineRefresh = undefined;
  loadWorkflowLink(elements["game-workflow"], `/api/me/campaigns/${active.campaignId}/levels/${active.level}/workflow-link`).catch(showError);
  renderGame();
}

async function reviewDailyOutcome(campaign) {
  const level = campaign.failed ? campaign.nextLevel : campaign.totalLevels;
  currentGame = await api(`/api/me/campaigns/${campaign.campaignId}/levels/${level}`);
  completionRefresh = undefined;
  completionAction = undefined;
  deadlineRefresh = undefined;
  loadWorkflowLink(elements["game-workflow"], `/api/me/campaigns/${campaign.campaignId}/levels/${level}/workflow-link`).catch(showError);
  renderGame();
}

async function exitGame() {
  await showCatalog();
}

function renderGame() {
  stopCampaignTicker();
  elements.stats.hidden = true;
  elements["point-shop"].hidden = true;
  elements.catalog.hidden = true;
  elements.complete.hidden = true;
  elements.game.hidden = false;
  const daily = catalog?.campaigns?.find((campaign) => campaign.campaignId === currentGame.campaignId)?.kind === "daily-challenge";
  elements["level-label"].textContent = `${daily ? "Daily Challenge · " : ""}Level ${currentGame.level}`;
  elements["level-title"].textContent = currentGame.title;
  elements.progress.textContent = `${currentGame.foundWords} / ${currentGame.totalWords} words resolved`;
  elements.event.hidden = !currentGame.specialEvent;
  if (currentGame.specialEvent) {
    elements.event.textContent = `${currentGame.specialEvent.name} · +${currentGame.specialEvent.bonusPoints} points`;
  }
  renderCrossword();
  renderRejectedWords();
  renderLetters();
  renderHintButton("letter", "Reveal one", currentGame.hints.letters, currentGame.hintPrices.letter);
  renderHintButton("brush", "Reveal two", currentGame.hints.brushes, currentGame.hintPrices.brush);
  renderHintButton("word", "Resolve word", currentGame.hints.words, currentGame.hintPrices.word);
  elements["hint-bonus-points"].textContent = `+${currentGame.hintBonus} points`;
  elements["accuracy-bonus-points"].textContent = `+${currentGame.accuracyBonus} points`;

  if (currentGame.timedOut) {
    stopSpeedBonusTicker();
    stopLevelDeadlineTicker();
    renderTimedOut(daily);
    beginCompletionRefresh();
  } else if (currentGame.complete) {
    stopSpeedBonusTicker();
    stopLevelDeadlineTicker();
    renderComplete();
    beginCompletionRefresh();
  } else {
    startSpeedBonusTicker();
    startLevelDeadlineTicker();
  }
}

function startLevelDeadlineTicker() {
  stopLevelDeadlineTicker();
  if (!currentGame.expiresAt) {
    elements["level-deadline"].hidden = true;
    return;
  }

  elements["level-deadline"].hidden = false;
  elements["level-deadline"].classList.remove("expired");
  const tick = () => {
    const secondsRemaining = Math.max(0, Math.ceil((Date.parse(currentGame.expiresAt) - Date.now()) / 1000));
    elements["level-deadline"].textContent = secondsRemaining
      ? `Hard limit · ${formatCountdown(secondsRemaining)}`
      : "Time expired";
    if (secondsRemaining) return;
    elements["level-deadline"].classList.add("expired");
    stopLevelDeadlineTicker();
    refreshExpiredLevel();
  };
  tick();
  if (Date.parse(currentGame.expiresAt) > Date.now()) {
    levelDeadlineTimer = setInterval(tick, 250);
  }
}

function stopLevelDeadlineTicker() {
  clearInterval(levelDeadlineTimer);
  levelDeadlineTimer = undefined;
}

function refreshExpiredLevel() {
  if (deadlineRefresh) return;
  const campaignId = currentGame.campaignId;
  const level = currentGame.level;
  deadlineRefresh = (async () => {
    for (let attempt = 0; attempt < 20; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 250));
      const latest = await api(`/api/me/campaigns/${campaignId}/levels/${level}`);
      if (latest.timedOut) {
        if (currentGame?.campaignId === campaignId && currentGame?.level === level) {
          currentGame = latest;
          renderGame();
        }
        return;
      }
    }
    throw new Error("The level timer expired, but its Workflow has not closed yet.");
  })();
  deadlineRefresh.catch(showError).finally(() => { deadlineRefresh = undefined; });
}

function startSpeedBonusTicker() {
  stopSpeedBonusTicker();
  const tick = () => {
    if (!renderSpeedBonus(Date.now())) stopSpeedBonusTicker();
  };
  tick();
  if ((currentGame.speedBonuses || []).some((tier) => Date.parse(tier.endsAt) >= Date.now())) {
    speedBonusTimer = setInterval(tick, 1000);
  }
}

function stopSpeedBonusTicker() {
  clearInterval(speedBonusTimer);
  speedBonusTimer = undefined;
}

function renderSpeedBonus(now) {
  const tiers = currentGame.speedBonuses || [];
  const tierIndex = tiers.findIndex((tier) => now <= Date.parse(tier.endsAt));
  if (tierIndex === -1) {
    elements["speed-bonus-points"].textContent = "0 points";
    elements["speed-bonus-countdown"].textContent = "Speed bonus window ended";
    return false;
  }

  const tier = tiers[tierIndex];
  const nextPoints = tiers[tierIndex + 1]?.points || 0;
  const secondsRemaining = Math.max(0, Math.ceil((Date.parse(tier.endsAt) - now) / 1000));
  elements["speed-bonus-points"].textContent = `+${tier.points} points`;
  elements["speed-bonus-countdown"].textContent = `${formatCountdown(secondsRemaining)} until ${nextPoints ? `+${nextPoints}` : "0"}`;
  return true;
}

function formatCountdown(totalSeconds) {
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = String(totalSeconds % 60).padStart(2, "0");
  return `${minutes}:${seconds}`;
}

function renderRejectedWords() {
  const words = currentGame.rejectedWords || [];
  elements["rejected-words"].replaceChildren(...words.map((word) => {
    const item = document.createElement("li");
    item.textContent = word;
    return item;
  }));
  elements["rejected-empty"].hidden = words.length !== 0;
}

function renderHintButton(hint, label, remaining, price) {
  const button = document.querySelector(`[data-hint="${hint}"]`);
  const paid = remaining === 0;
  button.textContent = paid ? `${label} · ${price} points` : `${label} · ${remaining} free`;
  button.disabled = paid && player.points < price;
  button.classList.toggle("paid", paid);
}

function renderCrossword() {
  renderCrosswordInto(elements.crossword, currentGame.cells);
}

function renderCrosswordInto(element, cells) {
  const maxRow = Math.max(...cells.map((cell) => cell.row));
  const maxCol = Math.max(...cells.map((cell) => cell.col));
  element.style.setProperty("--grid-columns", maxCol + 1);
  element.style.gridTemplateRows = `repeat(${maxRow + 1}, auto)`;
  element.style.gridTemplateColumns = `repeat(${maxCol + 1}, auto)`;
  element.innerHTML = cells.map((cell) => {
    const state = cell.missed ? "missed" : cell.hinted ? "hinted" : cell.revealed ? "revealed" : "";
    const label = cell.missed ? "Missed solution letter" : cell.hinted ? "Hint-revealed letter" : cell.revealed ? "Found word letter" : "Hidden letter";
    return `<div class="cell ${state}" aria-label="${label}" style="grid-row:${cell.row + 1};grid-column:${cell.col + 1}">${cell.letter || ""}</div>`;
  }).join("");
}

function renderLetters() {
  selectedIndexes = [];
  elements.guess.innerHTML = "&nbsp;";
  const letters = [...currentGame.letters];
  elements.letters.innerHTML = letters.map((letter, index) => {
    const angle = (Math.PI * 2 * index / letters.length) - Math.PI / 2;
    const left = 50 + Math.cos(angle) * 37;
    const top = 50 + Math.sin(angle) * 37;
    return `<button class="letter" data-index="${index}" style="left:${left}%;top:${top}%">${letter}</button>`;
  }).join("");
  elements.letters.querySelectorAll(".letter").forEach((button) => {
    button.addEventListener("click", () => selectLetter(Number(button.dataset.index)));
  });
}

function selectLetter(index) {
  if (selectedIndexes.includes(index)) return;
  selectedIndexes.push(index);
  elements.letters.querySelector(`[data-index="${index}"]`).classList.add("selected");
  elements.guess.textContent = selectedIndexes.map((i) => currentGame.letters[i]).join("");
}

function clearGuess() {
  selectedIndexes = [];
  elements.guess.innerHTML = "&nbsp;";
  elements.letters.querySelectorAll(".letter").forEach((button) => button.classList.remove("selected"));
}

async function submitGuess() {
  const word = selectedIndexes.map((index) => currentGame.letters[index]).join("");
  if (!word) return;
  const result = await api(`/api/me/campaigns/${currentGame.campaignId}/levels/${currentGame.level}/guesses`, {
    method: "POST",
    body: JSON.stringify({ requestId: requestID(), word }),
  });
  currentGame = result.game;
  showNotice({ found: "Word recorded in history.", already_found: "Already in history.", not_in_puzzle: "Not in the wordflow.", invalid_letters: "Those letters cannot make that word." }[result.outcome] || result.outcome);
  renderGame();
}

async function useHint(hint) {
  const remaining = {
    letter: currentGame.hints.letters,
    brush: currentGame.hints.brushes,
    word: currentGame.hints.words,
  }[hint];
  const buying = remaining === 0;
  try {
    const result = await api(`/api/me/campaigns/${currentGame.campaignId}/levels/${currentGame.level}/hints`, {
      method: "POST",
      body: JSON.stringify({ requestId: requestID(), hint }),
    });
    currentGame = result.game;
    if (result.pointsRemaining !== undefined) {
      player.points = result.pointsRemaining;
      renderPlayer();
    }
    const outcome = result.outcome.replaceAll("_", " ");
    showNotice(result.pointsSpent ? `Spent ${result.pointsSpent} points · ${outcome}` : outcome);
    renderGame();
  } catch (error) {
    if (buying) {
      try {
        player = await api("/api/me");
        renderPlayer();
        renderGame();
      } catch (_) {
        // Preserve the point-spend error shown to the player.
      }
    }
    throw error;
  }
}

async function buyStreakFreeze() {
  player = await api("/api/me/streak-freeze", {
    method: "POST",
    body: JSON.stringify({ requestId: requestID() }),
  });
  renderPlayer();
  if (currentGame) renderGame();
  showNotice("Streak freeze ready.");
}

async function shuffle() {
  currentGame = await api(`/api/me/campaigns/${currentGame.campaignId}/levels/${currentGame.level}/shuffle`, {
    method: "POST",
    body: JSON.stringify({ requestId: requestID() }),
  });
  renderGame();
}

function renderComplete() {
  elements.game.hidden = true;
  elements.complete.hidden = false;
  elements.complete.classList.remove("timed-out");
  elements["complete-eyebrow"].textContent = "Workflow completed";
  elements["complete-title"].textContent = "Execution succeeded.";
  elements["timeout-solution"].hidden = true;
  const score = currentGame.score;
  elements["complete-summary"].textContent = score
    ? `Level ${currentGame.level} completed in ${formatDuration(score.durationSeconds)}. `
      + `Earned ${score.points} points: ${score.basePoints} win + ${score.speedBonus} speed + `
      + `${score.accuracyBonus} accuracy + ${score.hintBonus} hint bonus.`
    : `Level ${currentGame.level} completed after ${currentGame.attempts} word attempt(s).`;
}

function renderTimedOut(daily) {
  elements.game.hidden = true;
  elements.complete.hidden = false;
  elements.complete.classList.add("timed-out");
  elements["complete-eyebrow"].textContent = daily ? "Daily challenge ended" : "Level attempt ended";
  elements["complete-title"].textContent = "Time expired.";
  elements["complete-summary"].textContent = daily
    ? `The hard time limit expired on level ${currentGame.level}. Today's attempt is now complete.`
    : `The hard time limit expired on level ${currentGame.level}.`;
  elements["timeout-solution"].hidden = false;
  renderCrosswordInto(elements["timeout-crossword"], currentGame.cells);
  elements["timeout-answers"].replaceChildren(...(currentGame.solutionWords || []).map((word) => {
    const answer = document.createElement("li");
    answer.className = word.found ? "found" : "missed";
    answer.textContent = word.answer;
    answer.title = word.found ? "Found before time expired" : "Missed before time expired";
    return answer;
  }));
}

function formatDuration(totalSeconds) {
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return minutes ? `${minutes}m ${seconds}s` : `${seconds}s`;
}

function beginCompletionRefresh() {
  if (completionRefresh) return;
  elements.continue.disabled = true;
  elements.continue.textContent = "Checking next level…";
  completionRefresh = prepareCompletionAction();
  completionRefresh.then(() => {
    elements.continue.disabled = false;
  }, (error) => {
    completionRefresh = undefined;
    elements.continue.disabled = false;
    elements.continue.textContent = "Try again";
    showError(error);
  });
}

async function prepareCompletionAction() {
  await refreshPlayerAfterCompletion();
  const latestCatalog = await api("/api/catalog");
  const campaign = latestCatalog.campaigns.find((item) => item.campaignId === currentGame.campaignId);
  const nextLevel = campaign?.levels.find((level) => level.status === "available");
  catalog = latestCatalog;
  completionAction = nextLevel
    ? { campaignId: campaign.campaignId, level: nextLevel.level }
    : undefined;
  elements.continue.textContent = nextLevel ? `Play level ${nextLevel.level}` : "Back to campaigns";
}

async function refreshPlayerAfterCompletion() {
  for (let attempt = 0; attempt < 40; attempt++) {
    await new Promise((resolve) => setTimeout(resolve, 250));
    const latestPlayer = await api("/api/me");
    if (!latestPlayer.activeGame) {
      player = latestPlayer;
      renderPlayer();
      loadWorkflowLink(elements["player-workflow"], "/api/me/workflow-link").catch(showError);
      return;
    }
  }
  throw new Error("The Player Workflow is still consuming the child result. Please try again.");
}

async function showNextWordflow() {
  if (!completionRefresh && player.activeGame) {
    beginCompletionRefresh();
  }
  if (completionRefresh) await completionRefresh;
  completionRefresh = undefined;
  if (completionAction) {
    const next = completionAction;
    completionAction = undefined;
    await startGame(next.campaignId, next.level);
    return;
  }
  await showCatalog(catalog);
}

async function loadWorkflowLink(element, path) {
  element.removeAttribute("href");
  element.setAttribute("aria-disabled", "true");
  const result = await api(path);
  element.href = result.url;
  element.removeAttribute("aria-disabled");
}

function showNotice(message) {
  clearTimeout(noticeTimer);
  elements.notice.hidden = false;
  elements.notice.textContent = message;
  noticeTimer = setTimeout(() => { elements.notice.hidden = true; }, 2400);
}

elements["buy-streak-freeze"].addEventListener("click", () => buyStreakFreeze().catch(showError));
elements["exit-game"].addEventListener("click", () => exitGame().catch(showError));
elements.logout.addEventListener("click", () => logOut().catch(showError));
elements["login-form"].addEventListener("submit", (event) => {
  event.preventDefault();
  authenticate("/api/login", event.currentTarget).catch(showError);
});
elements["signup-form"].addEventListener("submit", (event) => {
  event.preventDefault();
  authenticate("/api/signup", event.currentTarget).catch(showError);
});
elements.clear.addEventListener("click", clearGuess);
elements.submit.addEventListener("click", () => submitGuess().catch(showError));
elements.shuffle.addEventListener("click", () => shuffle().catch(showError));
elements.continue.addEventListener("click", () => showNextWordflow().catch(showError));
document.querySelectorAll("[data-hint]").forEach((button) => {
  button.addEventListener("click", () => useHint(button.dataset.hint).catch(showError));
});

function showError(error) {
  showNotice(error.message || String(error));
}

if (document.body.dataset.session === "authenticated") {
  openSession().catch(showError);
} else {
  showAuthentication();
}
