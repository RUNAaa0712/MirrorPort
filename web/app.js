const byId = (id) => document.getElementById(id);
const grid = byId("cards");
const add = byId("add");
const dialog = byId("obs-dialog");
const cards = [];
const maxSlots = 4;
const labels = {
  idle: ["未接続", "接続を待っています", "受信を開始してからiPhoneで受信機を選びます。"],
  starting: ["起動中", "受信機を起動しています", "接続コードの表示をお待ちください。"],
  waiting: ["接続待ち", "iPhoneからの接続を待っています", "iPhoneの「画面ミラーリング」で受信機を選びます。"],
  playing: ["表示中", "", ""],
  stopping: ["停止中", "受信機を停止しています", ""],
  error: ["受信エラー", "受信機が停止しました", "詳細は「受信設定」のログで確認できます。"],
};
const idle = { state: "idle" };

function readSettings() {
  try {
    return JSON.parse(localStorage.getItem("mirrorport.v2")) || {};
  } catch {
    return {};
  }
}

const saved = readSettings();
let layout = saved.layout === "list" ? "list" : "grid";
let availability;
let polling = false;
let closed = false;
let failed = false;
let toastTimer;
let selectedOBS;

function saveSettings() {
  try {
    localStorage.setItem("mirrorport.v2", JSON.stringify({
      layout,
      slots: cards.map((card) => ({
        name: card.name.value,
        fps: card.fps.value,
        audio: card.audio.value,
        display: card.display.value,
      })),
    }));
  } catch {
    // The dashboard works with storage disabled.
  }
}

function notify(message) {
  const feedback = byId("feedback");
  clearTimeout(toastTimer);
  feedback.hidden = false;
  feedback.textContent = message;
  toastTimer = setTimeout(() => { feedback.hidden = true; }, 3200);
}

function apiPath(card, command) {
  return "/api/airplay/" + card.slot + "/" + command;
}

function obsURL(card) {
  return new URL("/obs/" + card.slot, location.origin).href;
}

function isActive(state) {
  return !["idle", "error"].includes(state);
}

function addCard() {
  if (cards.length >= maxSlots) return;
  const slot = cards.length + 1;
  const article = byId("card-template").content.firstElementChild.cloneNode(true);
  const find = (selector) => article.querySelector(selector);
  const card = {
    slot, article, status: idle, busy: false, error: "", revision: 0, session: "", retry: 0,
    name: find(".name"), fps: find(".fps"), audio: find(".audio"), display: find(".display"),
    image: find("img"), stage: find(".stage"), state: find(".state"), title: find("h3"),
    meta: find(".device-meta"), placeholder: find(".placeholder"), pin: find(".pin"),
    start: find(".start"), stop: find(".stop"), fullscreen: find(".fullscreen"),
    snapshot: find(".snapshot"), feedback: find(".error"), log: find("pre"),
  };
  const preferences = saved.slots?.[slot - 1] || {};
  card.name.value = preferences.name || "MirrorPort " + slot;
  card.fps.value = ["24", "30", "60"].includes(preferences.fps) ? preferences.fps : "30";
  card.audio.value = ["on", "off"].includes(preferences.audio) ? preferences.audio : slot === 1 ? "on" : "off";
  card.display.value = ["auto", "browser", "window"].includes(preferences.display) ? preferences.display : "auto";
  find(".slot-mark").textContent = slot;
  article.setAttribute("aria-label", "受信機 " + slot);
  for (const input of [card.name, card.fps, card.audio, card.display]) {
    input.addEventListener("input", () => { saveSettings(); renderCard(card); });
  }
  card.start.addEventListener("click", () => action(card, async () => {
    card.status = await request(apiPath(card, "start"), {
      name: card.name.value, fps: Number(card.fps.value), display: card.display.value,
      muted: card.audio.value === "off",
    });
  }));
  card.stop.addEventListener("click", () => action(card, async () => {
    await request(apiPath(card, "stop"), {});
    card.status = { ...card.status, state: "stopping" };
  }));
  find(".obs-copy").addEventListener("click", () => copyOBS(card));
  find(".obs-guide").addEventListener("click", () => openOBS(card));
  card.fullscreen.addEventListener("click", () => action(card, () => card.stage.requestFullscreen()));
  card.snapshot.addEventListener("click", () => action(card, () => downloadSnapshot(card)));
  card.image.addEventListener("load", () => renderCard(card));
  card.image.addEventListener("error", () => { card.session = ""; });
  cards.push(card);
  grid.append(article);
}

function renderCard(card) {
  const status = card.status;
  const state = labels[status.state] ? status.state : "idle";
  const active = isActive(state);
  const playing = state === "playing" && status.display === "browser";
  const [label, text, hint] = labels[state];
  const width = playing ? card.image.naturalWidth : 0;
  card.title.textContent = status.name || card.name.value || "受信機 " + card.slot;
  card.meta.textContent = width
    ? width + " × " + card.image.naturalHeight + " · AirPlay"
    : status.display === "window" ? "Windowsウィンドウ" : "AirPlay";
  if (card.state.dataset.state !== state) {
    card.state.dataset.state = state;
    card.state.querySelector("span").textContent = label;
  }
  card.placeholder.hidden = playing;
  card.placeholder.querySelector("strong").textContent = text;
  card.placeholder.querySelector("p").textContent = state === "waiting" && status.display === "window"
    ? "接続後の映像は別のWindowsウィンドウに表示します。"
    : hint;
  card.image.hidden = !playing;
  card.pin.hidden = state !== "waiting" || !status.pin;
  card.pin.querySelector("strong").textContent = status.pin || "";
  card.start.hidden = active;
  card.start.disabled = closed || card.busy || !availability?.airplayInstalled ||
    (card.display.value === "browser" && !availability.ffmpegInstalled);
  card.stop.hidden = !active;
  card.stop.disabled = closed || card.busy || !availability || state === "stopping";
  card.fullscreen.disabled = !playing;
  card.snapshot.disabled = !playing || card.busy;
  for (const field of [card.name, card.fps, card.audio, card.display]) {
    field.disabled = closed || active || card.busy;
  }
  const error = card.error || status.error || "";
  if (card.feedback.textContent !== error) card.feedback.textContent = error;
  card.feedback.hidden = !error;
  const log = status.log || "受信開始後にログを表示します。";
  if (card.log.textContent !== log) card.log.textContent = log;
  if (playing && card.session !== status.id) {
    card.session = status.id;
    card.image.src = apiPath(card, "video") + "?" +
      new URLSearchParams({ id: card.session, retry: String(card.retry++) });
  } else if (!playing && card.image.hasAttribute("src")) {
    card.session = "";
    card.image.removeAttribute("src");
  }
}

function render() {
  for (const card of cards) renderCard(card);
  grid.dataset.layout = layout;
  const connection = byId("connection");
  const online = Boolean(availability) && !closed;
  connection.dataset.ready = online ? "yes" : "no";
  const connectionLabel = closed ? "停止済み" : online ? "ローカルで稼働中" : failed ? "接続できません" : "起動を確認中";
  if (connection.lastChild.nodeValue !== connectionLabel) connection.lastChild.nodeValue = connectionLabel;
  byId("quit").disabled = !online;
  byId("summary").textContent = "ブラウザー表示 " + cards.filter((card) => card.status.state === "playing").length + " 台";
  add.disabled = cards.length >= maxSlots || closed;
  const dependency = byId("dependency");
  const message = online && !availability.airplayInstalled
    ? "初回は、同じフォルダーの setup-airplay.cmd を実行してください。"
    : online && !availability.ffmpegInstalled
      ? "ブラウザー表示とOBS連携にはFFmpegが必要です。「自動」はWindowsウィンドウで受信します。"
      : "";
  dependency.hidden = !message;
  dependency.textContent = message;
  for (const button of document.querySelectorAll("button[data-layout]")) {
    button.setAttribute("aria-pressed", String(button.dataset.layout === layout));
  }
}

async function request(path, body) {
  const options = { cache: "no-store", signal: AbortSignal.timeout(5000) };
  if (body !== undefined) {
    options.method = "POST";
    options.headers = { "Content-Type": "application/json", "X-MirrorPort-Key": availability?.key || "" };
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path, options);
  if (response.status === 204) return;
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(payload.error || "操作を完了できませんでした。");
  return payload;
}

async function refresh() {
  if (closed || polling) return;
  polling = true;
  const revisions = cards.map((card) => card.revision);
  try {
    const result = await request("/api/status");
    if (closed) return;
    availability = result;
    failed = false;
    const visible = result.slots.reduce((count, status, index) => isActive(status.state) ? index + 1 : count, 0);
    while (cards.length < visible && cards.length < maxSlots) addCard();
    for (const card of cards) {
      if (!card.busy && (revisions[card.slot - 1] ?? 0) === card.revision) {
        card.status = result.slots[card.slot - 1] || idle;
      }
    }
  } catch {
    if (!closed) {
      availability = undefined;
      failed = true;
    }
  } finally {
    polling = false;
    render();
  }
}

async function action(card, run) {
  if (closed || card.busy) return;
  card.busy = true;
  card.revision++;
  card.error = "";
  renderCard(card);
  try {
    await run();
  } catch (error) {
    card.error = error.message;
  } finally {
    card.busy = false;
    render();
  }
}

async function downloadSnapshot(card) {
  const response = await fetch(apiPath(card, "snapshot"), { signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error("保存できる映像がありません。");
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = "mirrorport-" + card.slot + "-" + new Date().toISOString().replace(/[:.]/g, "-") + ".jpg";
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

function openOBS(card) {
  selectedOBS = card;
  byId("obs-title").textContent = "受信機 " + card.slot + " をOBSに追加";
  byId("obs-url").value = obsURL(card);
  byId("obs-open").href = obsURL(card);
  byId("dialog-copy").textContent = "URLをコピー";
  if (!dialog.open) dialog.showModal();
}

async function copyOBS(card) {
  try {
    await navigator.clipboard.writeText(obsURL(card));
    if (dialog.open && selectedOBS === card) byId("dialog-copy").textContent = "コピーしました";
    notify("受信機 " + card.slot + " のOBS URLをコピーしました");
  } catch {
    openOBS(card);
    byId("obs-url").select();
  }
}

for (let i = 0; i < Math.min(maxSlots, Math.max(2, saved.slots?.length || 0)); i++) addCard();
add.addEventListener("click", () => { addCard(); saveSettings(); render(); });
for (const button of document.querySelectorAll("button[data-layout]")) {
  button.addEventListener("click", () => { layout = button.dataset.layout; saveSettings(); render(); });
}
byId("dialog-copy").addEventListener("click", () => copyOBS(selectedOBS));
byId("obs-close").addEventListener("click", () => dialog.close());
byId("quit").addEventListener("click", async () => {
  try {
    await request("/api/quit", {});
    closed = true;
    for (const card of cards) card.status = idle;
    render();
  } catch (error) {
    notify(error.message);
  }
});

setInterval(refresh, 1000);
render();
refresh();
