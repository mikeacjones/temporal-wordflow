let player;
let catalog;
let currentGame;
let selectedIndexes = [];
let completionRefresh;
let noticeTimer;
let speedBonusTimer;
let campaignTimer;

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
  const session = await api(path, { method: "POST", body: JSON.stringify(input) });
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

async function showPlayer(initialCatalog, initialGame) {
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
  currentGame = undefined;
  elements.stats.hidden = false;
  elements["point-shop"].hidden = false;
  elements.game.hidden = true;
  elements.complete.hidden = true;
  elements.catalog.hidden = false;
  catalog = initialCatalog || await api("/api/catalog");
  renderCatalog();
}

function renderCatalog() {
  elements.campaigns.replaceChildren(...catalog.campaigns.map(renderCampaign));
  startCampaignTicker();
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
  progress.textContent = `${campaign.completedLevels} / ${campaign.totalLevels} levels completed`;

  const levels = document.createElement("div");
  levels.className = "campaign-levels";
  levels.replaceChildren(...campaign.levels.map((level) => {
    const item = document.createElement("span");
    item.className = `campaign-level ${level.status}`;
    item.title = `Level ${level.level}: ${level.title} · ${level.status}`;
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

function campaignCountdown(at) {
  const countdown = document.createElement("span");
  countdown.className = "campaign-countdown";
  countdown.dataset.countdownAt = at;
  return countdown;
}

function startCampaignTicker() {
  stopCampaignTicker();
  const buttons = [...elements.campaigns.querySelectorAll("[data-unlocks-at]")];
  const countdowns = [...elements.campaigns.querySelectorAll("[data-countdown-at]")];
  const tick = () => {
    let waiting = false;
    buttons.forEach((button) => {
      const remaining = Date.parse(button.dataset.unlocksAt) - Date.now();
      const countdown = button.querySelector(".campaign-unlock-countdown");
      if (remaining <= 0) {
        button.disabled = false;
        button.classList.remove("quiet");
        button.classList.add("primary");
        button.textContent = `Start level ${button.dataset.level}`;
        return;
      }
      countdown.textContent = formatUnlockCountdown(remaining);
      waiting = true;
    });
    for (const countdown of countdowns) {
      const remaining = Date.parse(countdown.dataset.countdownAt) - Date.now();
      if (remaining <= 0) {
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
  loadWorkflowLink(elements["game-workflow"], `/api/me/campaigns/${active.campaignId}/levels/${active.level}/workflow-link`).catch(showError);
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
  elements["level-label"].textContent = `Level ${currentGame.level}`;
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

  if (currentGame.complete) {
    stopSpeedBonusTicker();
    renderComplete();
    beginCompletionRefresh();
  } else {
    startSpeedBonusTicker();
  }
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
  const maxRow = Math.max(...currentGame.cells.map((cell) => cell.row));
  const maxCol = Math.max(...currentGame.cells.map((cell) => cell.col));
  elements.crossword.style.setProperty("--grid-columns", maxCol + 1);
  elements.crossword.style.gridTemplateRows = `repeat(${maxRow + 1}, auto)`;
  elements.crossword.style.gridTemplateColumns = `repeat(${maxCol + 1}, auto)`;
  elements.crossword.innerHTML = currentGame.cells.map((cell) => {
    const state = cell.hinted ? "hinted" : cell.revealed ? "revealed" : "";
    const label = cell.hinted ? "Hint-revealed letter" : cell.revealed ? "Found word letter" : "Hidden letter";
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
  const score = currentGame.score;
  elements["complete-summary"].textContent = score
    ? `Level ${currentGame.level} completed in ${formatDuration(score.durationSeconds)}. `
      + `Earned ${score.points} points: ${score.basePoints} win + ${score.speedBonus} speed + `
      + `${score.accuracyBonus} accuracy + ${score.hintBonus} hint bonus.`
    : `Level ${currentGame.level} completed after ${currentGame.attempts} word attempt(s).`;
}

function formatDuration(totalSeconds) {
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return minutes ? `${minutes}m ${seconds}s` : `${seconds}s`;
}

function beginCompletionRefresh() {
  if (completionRefresh) return;
  elements.continue.disabled = true;
  completionRefresh = refreshPlayerAfterCompletion();
  completionRefresh.then(() => {
    elements.continue.disabled = false;
  }, (error) => {
    completionRefresh = undefined;
    elements.continue.disabled = false;
    showError(error);
  });
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
  if (player.activeGame) {
    if (!completionRefresh) beginCompletionRefresh();
    await completionRefresh;
  }
  completionRefresh = undefined;
  await showCatalog();
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

openSession().catch(showError);
