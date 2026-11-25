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
  error: ["受信エラー", "受信機が停止しました", "詳細は「高度な設定」の受信ログで確認できます。"],
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
let layout = ["list", "focus"].includes(saved.layout) ? saved.layout : "grid";
let focusedSlot = Number.isInteger(saved.focusedSlot) ? saved.focusedSlot : 0;
let availability;
let polling = false;
let closed = false;
let failed = false;
let toastTimer;
let selectedOBS;
const deviceLists = {};
const loadingDevices = new Set();
const attemptedDevices = new Set();

function saveSettings() {
  try {
    localStorage.setItem("mirrorport.v2", JSON.stringify({
      layout,
      focusedSlot,
      slots: cards.map((card) => ({
        name: card.name.value,
        fps: card.fps.value,
        audio: card.muted ? "off" : "on",
        volume: card.volume.value,
        display: card.display.value,
        source: card.source.value,
        resolution: card.resolution.value,
        quality: card.quality.value,
        airplayTiming: card.airplayTiming.value,
        captureVideo: card.captureVideoID,
        captureAudio: card.captureAudioID,
        usbDevice: card.usbDeviceID,
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
    name: find(".name"), fps: find(".fps"), display: find(".display"), source: find(".source"),
    displayField: find(".display-field"), resolution: find(".resolution"), quality: find(".quality"),
    resolutionLabel: find(".resolution-label"), airplayTiming: find(".airplay-timing"),
    airplayTimingField: find(".airplay-timing-field"),
    volume: find(".volume"), volumeValue: find(".volume-value"), mute: find(".mute"),
    captureFields: find(".capture-fields"), captureVideo: find(".capture-video"),
    captureAudio: find(".capture-audio"), captureRefresh: find(".capture-refresh"),
    deviceLabel: find(".input-device-label"), captureAudioField: find(".capture-audio-field"), usbHelp: find(".usb-help"),
    image: find("img"), stage: find(".stage"), state: find(".state"), title: find("h3"),
    meta: find(".device-meta"), placeholder: find(".placeholder"), pin: find(".pin"),
    start: find(".start"), stop: find(".stop"), fullscreen: find(".fullscreen"),
    snapshot: find(".snapshot"), feedback: find(".error"), log: find("pre"),
    mosaic: find(".mosaic-select"), mosaicClear: find(".mosaic-clear"), optionsLock: find(".options-lock"),
    layer: find(".mosaic-layer"), selectionHint: find(".selection-hint"),
  };
  const preferences = saved.slots?.[slot - 1] || {};
  card.name.value = preferences.name || "MirrorPort " + slot;
  card.fps.value = ["24", "30", "60"].includes(preferences.fps) ? preferences.fps : "60";
  card.muted = ["on", "off"].includes(preferences.audio) ? preferences.audio === "off" : slot !== 1;
  card.volume.value = preferences.volume ?? "100";
  card.display.value = ["auto", "browser", "window"].includes(preferences.display) ? preferences.display : "auto";
  card.source.value = ["capture", "usb"].includes(preferences.source) ? preferences.source : "airplay";
  card.resolution.value = ["1280x720", "1920x1080"].includes(preferences.resolution) ? preferences.resolution : "";
  card.quality.value = ["3", "5", "8"].includes(preferences.quality) ? preferences.quality : "5";
  card.airplayTiming.value = preferences.airplayTiming === "sync" ? "sync" : "low";
  card.captureVideoID = preferences.captureVideo || "";
  card.captureAudioID = preferences.captureAudio || "";
  card.usbDeviceID = preferences.usbDevice || "";
  fillSourceDevices(card);
  find(".slot-mark").textContent = slot;
  article.setAttribute("aria-label", "受信機 " + slot);
  article.id = "receiver-" + slot;
  const options = find(".options");
  const optionsTitle = options.querySelector("h2");
  optionsTitle.id = "options-title-" + slot;
  optionsTitle.textContent = "受信機 " + slot + " の高度な設定";
  options.setAttribute("aria-labelledby", optionsTitle.id);
  find(".options-open").addEventListener("click", () => options.showModal());
  find(".options-close").addEventListener("click", () => options.close());
  card.focusButton = document.createElement("button");
  card.focusButton.type = "button";
  card.focusButton.setAttribute("aria-controls", article.id);
  card.focusButton.addEventListener("click", () => {
    focusedSlot = slot;
    saveSettings();
    render();
  });
  byId("focus-picker").append(card.focusButton);
  for (const input of [card.name, card.fps, card.display, card.resolution, card.quality, card.airplayTiming]) {
    input.addEventListener("input", () => { saveSettings(); renderCard(card); });
  }
  card.source.addEventListener("change", () => {
    card.error = "";
    saveSettings();
    fillSourceDevices(card);
    render();
    if (!deviceLists[card.source.value]) refreshDevices(card.source.value);
  });
  card.captureRefresh.addEventListener("click", () => refreshDevices(card.source.value));
  card.captureVideo.addEventListener("change", () => {
    if (card.source.value === "usb") card.usbDeviceID = card.captureVideo.value;
    else card.captureVideoID = card.captureVideo.value;
    saveSettings();
    renderCard(card);
  });
  card.captureAudio.addEventListener("change", () => {
    card.captureAudioID = card.captureAudio.value;
    saveSettings();
  });
  card.volume.addEventListener("input", () => showVolume(card));
  card.volume.addEventListener("change", () => updateAudio(card, card.muted));
  card.mute.addEventListener("click", () => updateAudio(card, !card.muted));
  card.start.addEventListener("click", () => action(card, async () => {
    const source = card.source.value;
    card.status = await request(apiPath(card, "start"), {
      name: card.name.value, fps: Number(card.fps.value), display: source === "airplay" ? card.display.value : "browser",
      source, resolution: card.resolution.value, jpegQuality: Number(card.quality.value),
      airplayTiming: card.airplayTiming.value,
      videoDevice: source === "airplay" ? "" : card.captureVideo.value, audioDevice: source === "capture" ? card.captureAudio.value : "",
      muted: card.muted, volume: Number(card.volume.value),
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
  card.mosaic.addEventListener("click", () => {
    card.selecting = !card.selecting;
    card.drag = null;
    renderCard(card);
  });
  card.mosaicClear.addEventListener("click", () => changeMosaic(card, null));
  setupMosaicSelection(card);
  card.image.addEventListener("load", () => renderCard(card));
  card.image.addEventListener("error", () => { card.session = ""; });
  cards.push(card);
  grid.append(article);
}

function renderCard(card) {
  const status = card.status;
  const state = labels[status.state] ? status.state : "idle";
  const active = isActive(state);
  const visible = layout !== "focus" || Number(focusedSlot) === card.slot;
  const playing = visible && state === "playing" && status.display === "browser";
  const source = active ? status.source || card.source.value : card.source.value;
  const capture = source === "capture";
  const usb = source === "usb";
  const wired = usb || capture;
  const mode = usb ? "USB直接接続" : capture ? "HDMIキャプチャー" : "AirPlay";
  const [label, text, hint] = labels[state];
  const width = playing ? card.image.naturalWidth : 0;
  card.article.hidden = !visible;
  card.article.dataset.state = state;
  card.title.textContent = status.name || card.name.value || "受信機 " + card.slot;
  if (card.focusButton.textContent !== card.title.textContent) card.focusButton.textContent = card.title.textContent;
  card.focusButton.dataset.state = state;
  card.focusButton.setAttribute("aria-pressed", String(focusedSlot === card.slot));
  card.meta.textContent = width
    ? width + " × " + card.image.naturalHeight + " · " + mode
    : status.display === "window" ? "Windowsウィンドウ" : mode;
  if (card.state.dataset.state !== state) {
    card.state.dataset.state = state;
    card.state.querySelector("span").textContent = label;
  }
  card.placeholder.hidden = playing;
  card.placeholder.querySelector("strong").textContent = text;
  card.placeholder.querySelector("p").textContent = capture && ["idle", "starting"].includes(state)
    ? "iPhoneをHDMIでキャプチャーデバイスにつなぎ、機器を選びます。"
    : usb && ["idle", "starting"].includes(state)
      ? "iPhoneとPCをデータ通信対応のUSBケーブルでつなぎ、端末を選びます。"
    : status.display === "window" && ["waiting", "playing"].includes(state)
      ? "映像は別のWindowsウィンドウに表示します。"
      : hint;
  if (capture && state === "starting") card.placeholder.querySelector("strong").textContent = "有線映像を待っています";
  if (usb && state === "starting") card.placeholder.querySelector("strong").textContent = "USB映像を待っています";
  card.image.hidden = !playing;
  card.pin.hidden = state !== "waiting" || !status.pin;
  card.pin.querySelector("strong").textContent = status.pin || "";
  card.start.hidden = active;
  card.start.disabled = closed || card.busy || !availability || (wired
    ? !deviceSourceAvailable(source) || !card.captureVideo.value
    : !availability.airplayInstalled || (card.display.value === "browser" && !availability.ffmpegInstalled));
  card.stop.hidden = !active;
  card.stop.disabled = closed || card.busy || !availability || state === "stopping";
  card.fullscreen.disabled = !playing;
  card.snapshot.disabled = !playing || card.busy;
  for (const field of [card.name, card.fps, card.display, card.source, card.resolution, card.quality, card.airplayTiming, card.captureVideo, card.captureAudio]) {
    field.disabled = closed || active || card.busy;
  }
  card.captureFields.hidden = !wired;
  card.optionsLock.hidden = !active;
  card.captureAudioField.hidden = !capture;
  card.usbHelp.hidden = !usb;
  card.usbHelp.textContent = availability?.usbInstalled
    ? "初回はiPhoneをロック解除して「信頼」を押し、iPhoneMirrorのドライバー管理ツールで対象端末を準備してください。"
    : "USB受信エンジンのセットアップ手順はREADMEで確認できます。";
  card.deviceLabel.textContent = usb ? "USB接続したiPhone" : "映像デバイス";
  card.resolutionLabel.textContent = usb ? "出力解像度" : "入力解像度";
  card.captureRefresh.disabled = closed || !deviceSourceAvailable(source) || loadingDevices.has(source) || active || card.busy;
  card.displayField.hidden = wired;
  card.airplayTimingField.hidden = wired;
  card.quality.disabled ||= !wired && card.display.value === "window";
  card.mute.disabled = card.volume.disabled = closed || card.busy || (active
    ? !status.audio || state === "stopping"
    : capture && !card.captureAudio.value);
  showVolume(card, active && status.audio ? status.audio.muted : card.muted);
  if (!playing) {
    card.selecting = false;
    card.drag = null;
  }
  card.layer.hidden = !card.selecting;
  card.selectionHint.hidden = !card.selecting;
  card.mosaic.textContent = card.selecting ? "範囲選択を中止" : status.mosaic ? "モザイクを変更" : "モザイク";
  card.mosaic.disabled = !playing || card.busy || !width;
  card.mosaic.setAttribute("aria-pressed", String(Boolean(card.selecting)));
  card.mosaicClear.hidden = !status.mosaic;
  card.mosaicClear.disabled = !active || state === "stopping" || card.busy;
  drawMosaicSelection(card);
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
  if (layout === "focus" && !cards.some((card) => card.slot === focusedSlot)) {
    focusedSlot = (cards.find((card) => card.status.state === "playing") || cards[0])?.slot;
  }
  byId("focus-picker").hidden = layout !== "focus";
  for (const card of cards) renderCard(card);
  grid.dataset.layout = layout;
  const connection = byId("connection");
  const online = Boolean(availability) && !closed;
  connection.dataset.ready = online ? "yes" : "no";
  const connectionLabel = closed ? "停止済み" : online ? "ローカルで稼働中" : failed ? "接続できません" : "起動を確認中";
  if (connection.lastChild.nodeValue !== connectionLabel) connection.lastChild.nodeValue = connectionLabel;
  byId("quit").disabled = !online;
  byId("summary").textContent = "表示中 " + cards.filter((card) => card.status.state === "playing").length + " 台";
  byId("capacity").textContent = cards.length + " / " + maxSlots + " 台";
  add.disabled = cards.length >= maxSlots || closed;
  const dependency = byId("dependency");
  const notices = [];
  if (online && !availability.airplayInstalled && cards.some((card) => card.source.value === "airplay")) {
    notices.push("AirPlayを使うには、同じフォルダーの setup-airplay.cmd を実行してください。");
  }
  if (online && !availability.ffmpegInstalled) {
    notices.push("HDMI入力とAirPlayのブラウザー・OBS表示にはFFmpegが必要です。AirPlayの「自動」はWindowsウィンドウで受信します。");
  }
  const message = notices.join(" ");
  dependency.hidden = !message;
  dependency.textContent = message;
  for (const button of document.querySelectorAll("button[data-layout]")) {
    button.setAttribute("aria-pressed", String(button.dataset.layout === layout));
  }
}

const metrics = [...document.querySelectorAll("[data-metric]")].map((item) => ({
  item, key: item.dataset.metric, value: item.querySelector(".metric-value"),
  note: item.querySelector(".metric-note"), canvas: item.querySelector("canvas"),
}));
const metricLabels = {
  cpu: "全体の使用率", memory: "物理メモリ",
  gpu: "最も負荷の高いエンジン", disk: "最も負荷の高いドライブ",
};
const percentage = new Intl.NumberFormat("ja-JP", { maximumFractionDigits: 1 });
const memoryUnits = new Intl.NumberFormat("ja-JP", { maximumFractionDigits: 1 });
const updateTime = new Intl.DateTimeFormat("ja-JP", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });

function renderPerformance(snapshot) {
  const samples = snapshot?.samples || [];
  const last = samples.at(-1);
  const notes = {
    ...metricLabels,
    cpu: snapshot?.logicalCPUs ? snapshot.logicalCPUs + " 論理CPU" : metricLabels.cpu,
    memory: snapshot?.memoryTotal ? [snapshot.memoryUsed, snapshot.memoryTotal]
      .map((bytes) => memoryUnits.format(bytes / (1024 ** 3))).join(" / ") + " GiB 使用中" : metricLabels.memory,
  };
  byId("performance-status").textContent = last ? "直近60秒 · " + updateTime.format(last.at) + " 更新"
    : closed ? "測定を停止しました" : failed ? "PCの状態を取得できません" : "直近60秒 · 測定中";
  for (const metric of metrics) {
    const value = last?.[metric.key];
    metric.value.textContent = Number.isFinite(value) ? percentage.format(value) + "%" : "—";
    metric.note.textContent = last && !Number.isFinite(value) && samples.length > 2 ? "測定値を取得できません" : notes[metric.key];
    metric.item.dataset.load = value >= 85 ? "high" : value >= 65 ? "elevated" : "normal";
    drawTrace(metric.canvas, samples, metric.key);
  }
}

function drawTrace(canvas, samples, key) {
  const width = canvas.clientWidth;
  const height = canvas.clientHeight;
  if (!width || !height) return;
  const density = Math.min(window.devicePixelRatio || 1, 2);
  const pixelWidth = Math.round(width * density);
  const pixelHeight = Math.round(height * density);
  if (canvas.width !== pixelWidth || canvas.height !== pixelHeight) {
    canvas.width = pixelWidth;
    canvas.height = pixelHeight;
  }
  const context = canvas.getContext("2d");
  if (!context) return;
  context.setTransform(density, 0, 0, density, 0, 0);
  context.clearRect(0, 0, width, height);
  context.strokeStyle = "#718b7850";
  context.lineWidth = 1;
  context.beginPath();
  for (const y of [2, height / 2, height - 2]) {
    context.moveTo(0, y);
    context.lineTo(width, y);
  }
  context.stroke();
  if (!samples.length) return;
  const end = samples.at(-1).at;
  const start = end - 59000;
  const segments = [[]];
  let previous;
  for (const sample of samples) {
    if (sample.at < start) continue;
    const value = sample[key];
    if (!Number.isFinite(value) || (previous && sample.at - previous > 2500)) {
      if (segments.at(-1).length) segments.push([]);
    }
    if (Number.isFinite(value)) {
      segments.at(-1).push([(width - 4) * (sample.at - start) / 59000 + 2, height - 2 - (height - 4) * value / 100]);
    }
    previous = sample.at;
  }
  const tint = getComputedStyle(canvas).color;
  const gradient = context.createLinearGradient(0, 0, 0, height);
  gradient.addColorStop(0, "rgba(217, 250, 133, .23)");
  gradient.addColorStop(1, "rgba(217, 250, 133, .02)");
  for (const points of segments) {
    if (!points.length) continue;
    const [firstX, firstY] = points[0];
    const [lastX, lastY] = points.at(-1);
    context.beginPath();
    context.moveTo(firstX, firstY);
    for (const [x, y] of points.slice(1)) context.lineTo(x, y);
    context.lineTo(lastX, height);
    context.lineTo(firstX, height);
    context.closePath();
    context.fillStyle = gradient;
    context.fill();
    context.beginPath();
    context.moveTo(firstX, firstY);
    for (const [x, y] of points.slice(1)) context.lineTo(x, y);
    context.lineWidth = 1.6;
    context.strokeStyle = tint;
    context.stroke();
    context.beginPath();
    context.arc(lastX, lastY, 2, 0, 2 * Math.PI);
    context.fillStyle = tint;
    context.fill();
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
        const next = result.slots[card.slot - 1] || idle;
        if (next.audio && (next.id !== card.status.id || next.audio.volume !== card.status.audio?.volume || next.audio.muted !== card.status.audio?.muted)) {
          card.volume.value = next.audio.volume;
          card.muted = next.audio.muted;
        }
        card.status = next;
      }
    }
    for (const source of ["capture", "usb"]) {
      if (!attemptedDevices.has(source) && deviceSourceAvailable(source) && cards.some((card) => card.source.value === source)) {
        refreshDevices(source);
      }
    }
  } catch {
    if (!closed) {
      availability = undefined;
      failed = true;
    }
  } finally {
    polling = false;
    renderPerformance(availability?.performance);
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

function deviceSourceAvailable(source) {
  return source === "usb" ? availability?.usbInstalled : source === "capture" && availability?.ffmpegInstalled;
}

function fillSourceDevices(card) {
  const source = card.source.value;
  const list = deviceLists[source];
  const fill = (select, devices, empty, id) => {
    const options = devices.map((device) => new Option(device.name, device.id));
    select.replaceChildren(new Option(empty, ""), ...options);
    if (devices.some((device) => device.id === id)) select.value = id;
  };
  const missing = source === "usb" ? "USB接続のiPhoneが見つかりません" : "映像デバイスが見つかりません";
  fill(card.captureVideo, list?.video || [],
    list && !list.video.length ? missing : "選択してください",
    source === "usb" ? card.usbDeviceID : card.captureVideoID);
  fill(card.captureAudio, list?.audio || [], "なし", card.captureAudioID);
}

async function refreshDevices(source) {
  if (loadingDevices.has(source) || !deviceSourceAvailable(source)) return;
  loadingDevices.add(source);
  attemptedDevices.add(source);
  render();
  try {
    deviceLists[source] = await request("/api/" + source + "/devices");
    for (const card of cards) {
      if (card.source.value === source) fillSourceDevices(card);
    }
  } catch (error) {
    notify(error.message);
  } finally {
    loadingDevices.delete(source);
    render();
  }
}

function showVolume(card, muted = card.muted) {
  card.volumeValue.value = card.volume.value + "%";
  card.mute.setAttribute("aria-pressed", String(muted));
}

function updateAudio(card, muted) {
  const volume = Number(card.volume.value);
  if (!isActive(card.status.state)) {
    card.muted = muted;
    saveSettings();
    renderCard(card);
    return;
  }
  action(card, async () => {
    try {
      await request(apiPath(card, "audio"), { id: card.status.id, muted, volume });
    } catch (error) {
      card.volume.value = card.status.audio?.volume ?? 100;
      throw error;
    }
    card.status = { ...card.status, audio: { muted, volume } };
    card.muted = muted;
    saveSettings();
  });
}

function changeMosaic(card, region) {
  card.selecting = false;
  card.drag = null;
  action(card, async () => {
    await request(apiPath(card, "mosaic"), { id: card.status.id, region });
    card.status = { ...card.status, mosaic: region };
  });
}

const regionUnits = 10000;

function containedVideo(card) {
  const box = card.layer.getBoundingClientRect();
  const { naturalWidth, naturalHeight } = card.image;
  if (!naturalWidth || !naturalHeight) return null;
  const scale = Math.min(box.width / naturalWidth, box.height / naturalHeight);
  const width = naturalWidth * scale;
  const height = naturalHeight * scale;
  return { width, height, x: (box.width - width) / 2, y: (box.height - height) / 2, box };
}

function pointOnVideo(card, event, clamp = false) {
  const video = containedVideo(card);
  if (!video || !video.width || !video.height) return null;
  const x = (event.clientX - video.box.left - video.x) / video.width;
  const y = (event.clientY - video.box.top - video.y) / video.height;
  if (!clamp && (x < 0 || y < 0 || x > 1 || y > 1)) return null;
  const toUnits = (value) => Math.round(Math.min(1, Math.max(0, value)) * regionUnits);
  return { x: toUnits(x), y: toUnits(y) };
}

function makeRegion(a, b) {
  return {
    left: Math.min(a.x, b.x), right: Math.max(a.x, b.x),
    top: Math.min(a.y, b.y), bottom: Math.max(a.y, b.y),
  };
}

function drawMosaicSelection(card) {
  if (!card.selecting) return;
  const video = containedVideo(card);
  if (!video) return;
  const { layer, drag } = card;
  layer.width = Math.round(video.box.width);
  layer.height = Math.round(video.box.height);
  const region = drag ? makeRegion(drag.start, drag.end) : card.status.mosaic;
  if (!region) return;
  const ctx = layer.getContext("2d");
  const x = video.x + region.left / regionUnits * video.width;
  const y = video.y + region.top / regionUnits * video.height;
  const width = (region.right - region.left) / regionUnits * video.width;
  const height = (region.bottom - region.top) / regionUnits * video.height;
  ctx.fillStyle = "#69e9b844";
  ctx.strokeStyle = "#a9f2d7";
  ctx.lineWidth = 2;
  ctx.fillRect(x, y, width, height);
  ctx.strokeRect(x, y, width, height);
}

function setupMosaicSelection(card) {
  const { layer } = card;
  new ResizeObserver(() => drawMosaicSelection(card)).observe(card.stage);
  layer.addEventListener("pointerdown", (event) => {
    if (event.button !== 0 || card.drag) return;
    const start = pointOnVideo(card, event);
    if (!start) return;
    layer.setPointerCapture(event.pointerId);
    card.drag = { start, end: start, id: event.pointerId };
    drawMosaicSelection(card);
  });
  layer.addEventListener("pointermove", (event) => {
    if (card.drag?.id !== event.pointerId) return;
    card.drag.end = pointOnVideo(card, event, true) || card.drag.end;
    drawMosaicSelection(card);
  });
  layer.addEventListener("pointerup", (event) => {
    if (card.drag?.id !== event.pointerId) return;
    const { start, end } = card.drag;
    const region = makeRegion(start, pointOnVideo(card, event, true) || end);
    card.drag = null;
    if (region.right > region.left && region.bottom > region.top) changeMosaic(card, region);
    else drawMosaicSelection(card);
  });
  layer.addEventListener("lostpointercapture", () => {
    card.drag = null;
    drawMosaicSelection(card);
  });
  card.article.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || !card.selecting) return;
    card.selecting = false;
    card.drag = null;
    renderCard(card);
  });
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
add.addEventListener("click", () => {
  addCard();
  const card = cards.at(-1);
  if (layout === "focus") focusedSlot = card.slot;
  saveSettings();
  render();
  card.source.focus();
});
for (const button of document.querySelectorAll("button[data-layout]")) {
  button.addEventListener("click", () => { layout = button.dataset.layout; render(); saveSettings(); });
}
byId("dialog-copy").addEventListener("click", () => copyOBS(selectedOBS));
byId("obs-close").addEventListener("click", () => dialog.close());
byId("quit").addEventListener("click", async () => {
  try {
    await request("/api/quit", {});
    closed = true;
    for (const card of cards) card.status = idle;
    renderPerformance();
    render();
  } catch (error) {
    notify(error.message);
  }
});

setInterval(refresh, 1000);
render();
refresh();
