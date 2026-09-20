const entriesElement = document.querySelector("#leaderboard-entries");
const emptyElement = document.querySelector("#leaderboard-empty");
const statusElement = document.querySelector("#leaderboard-status");
let loading = false;

async function refreshLeaderboard() {
  if (loading) return;
  loading = true;
  try {
    const response = await fetch("/api/leaderboard", { headers: { Accept: "application/json" } });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || "Leaderboard unavailable");
    renderLeaderboard(body.entries || []);
    statusElement.textContent = `Live · updated ${new Date().toLocaleTimeString()}`;
  } catch (error) {
    statusElement.textContent = error.message || String(error);
  } finally {
    loading = false;
  }
}

function renderLeaderboard(entries) {
  entriesElement.replaceChildren(...entries.slice(0, 15).map((entry) => {
    const row = document.createElement("li");
    row.className = "leaderboard-row";

    const rank = document.createElement("strong");
    rank.textContent = `#${entry.rank}`;
    const name = document.createElement("span");
    name.textContent = entry.displayName;
    const levels = document.createElement("span");
    levels.textContent = entry.completedLevels;
    const points = document.createElement("strong");
    points.textContent = entry.lifetimePointsEarned;
    row.append(rank, name, levels, points);
    return row;
  }));
  emptyElement.hidden = entries.length !== 0;
}

refreshLeaderboard();
setInterval(refreshLeaderboard, 3000);
