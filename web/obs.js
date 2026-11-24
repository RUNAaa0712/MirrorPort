const image = document.getElementById("video");
const slot = location.pathname.split("/").pop();
let session = "";
let retry = 0;

function clear() {
  image.hidden = true;
  image.removeAttribute("src");
  session = "";
}

image.addEventListener("error", clear);

async function update() {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 3000);
  try {
    const response = await fetch("/obs/" + slot + "/state", { cache: "no-store", signal: controller.signal });
    if (!response.ok) throw new Error("OBS source is unavailable");
    const status = await response.json();
    if (status.state !== "playing" || status.display !== "browser" || !status.id) {
      if (session) clear();
    } else if (session !== status.id) {
      clear();
      session = status.id;
      image.src = "/api/airplay/" + slot + "/video?" + new URLSearchParams({ id: session, retry: String(retry++) });
      image.hidden = false;
    }
  } catch {
    clear();
  } finally {
    clearTimeout(timeout);
    setTimeout(update, 650);
  }
}

update();
