// Headless browser smoke test for the file transfer panel.
// Usage: node scripts/web_smoke.js <baseURL> <sessionID> <workDir>
// It drives Edge via the Chrome DevTools Protocol (no npm dependencies).
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const baseURL = process.argv[2];
const sessionID = process.argv[3];
const workDir = process.argv[4];
const EDGE = "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe";

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function main() {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "ezterm-cdp-"));
  const port = 9333;
  const child = spawn(
    EDGE,
    [
      "--headless=new",
      `--remote-debugging-port=${port}`,
      `--user-data-dir=${profile}`,
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-gpu",
      "about:blank",
    ],
    { stdio: "ignore" },
  );

  let wsURL = null;
  for (let i = 0; i < 60 && !wsURL; i += 1) {
    await sleep(250);
    try {
      const res = await fetch(`http://127.0.0.1:${port}/json/version`);
      const info = await res.json();
      wsURL = info.webSocketDebuggerUrl;
    } catch (_) {
      /* not up yet */
    }
  }
  if (!wsURL) {
    throw new Error("Edge did not expose a debugging endpoint");
  }

  const ws = new WebSocket(wsURL);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", reject, { once: true });
  });

  let nextID = 1;
  const pending = new Map();
  const consoleErrors = [];
  ws.addEventListener("message", (event) => {
    const msg = JSON.parse(event.data);
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      if (msg.error) reject(new Error(JSON.stringify(msg.error)));
      else resolve(msg.result);
      return;
    }
    if (msg.method === "Runtime.exceptionThrown") {
      consoleErrors.push(msg.params.exceptionDetails.text || "exception");
    }
    if (msg.method === "Runtime.consoleAPICalled" && msg.params.type === "error") {
      consoleErrors.push(msg.params.args.map((a) => a.value || a.description).join(" "));
    }
  });

  function send(method, params, sessionId) {
    const id = nextID++;
    const payload = { id, method, params: params || {} };
    if (sessionId) payload.sessionId = sessionId;
    ws.send(JSON.stringify(payload));
    return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
  }

  const { targetId } = await send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
  await send("Runtime.enable", {}, sessionId);
  await send("Page.enable", {}, sessionId);
  await send("Page.navigate", { url: `${baseURL}/web/${sessionID}` }, sessionId);
  await sleep(2500);

  async function evaluate(expression) {
    const result = await send(
      "Runtime.evaluate",
      { expression, awaitPromise: true, returnByValue: true },
      sessionId,
    );
    if (result.exceptionDetails) {
      throw new Error(result.exceptionDetails.text + " :: " + expression);
    }
    return result.result.value;
  }

  const results = {};
  globalThis.__partial = results;

  async function waitFor(expression, label, timeoutMs) {
    const deadline = Date.now() + (timeoutMs || 15000);
    while (Date.now() < deadline) {
      const value = await evaluate(expression);
      if (value) return value;
      await sleep(250);
    }
    throw new Error(`timed out waiting for ${label}`);
  }
  results.filesButtonVisible = await evaluate(
    `(() => { const b = document.getElementById("files-open"); return b ? !b.hidden : "missing"; })()`,
  );
  if (results.filesButtonVisible !== true) {
    throw new Error(`Files button not visible: ${JSON.stringify(results.filesButtonVisible)}`);
  }

  await evaluate(`document.getElementById("files-open").click()`);
  await sleep(300);
  results.modalOpen = await evaluate(`!document.getElementById("files-modal").hidden`);
  // The first SSH listing opens the SFTP subsystem and can take a moment.
  await waitFor(
    `(() => {
      const error = document.getElementById("files-error");
      if (error && !error.hidden) return "error";
      return document.getElementById("files-path").textContent !== "" ? "ready" : "";
    })()`,
    "the initial listing",
    20000,
  );
  results.path = await evaluate(`document.getElementById("files-path").textContent`);
  results.rowCount = await evaluate(`document.querySelectorAll(".files-row").length`);
  results.initialError = await evaluate(`document.getElementById("files-error").hidden ? "" : document.getElementById("files-error").textContent`);
  if (results.initialError) {
    throw new Error(`initial listing failed: ${results.initialError}`);
  }

  // Create a folder through the UI.
  const folder = "smoke-dir-" + Date.now();
  await evaluate(`document.getElementById("files-newdir").click()`);
  await sleep(200);
  await evaluate(`(() => {
    const input = document.getElementById("files-newdir-name");
    input.value = ${JSON.stringify(folder)};
    document.getElementById("files-newdir-create").click();
  })()`);
  await waitFor(
    `(() => {
      const error = document.getElementById("files-error");
      if (error && !error.hidden) return "error";
      return Array.from(document.querySelectorAll(".files-name")).some((n) => n.textContent.includes(${JSON.stringify(folder)})) ? "ready" : "";
    })()`,
    "the new folder",
    20000,
  );
  results.afterMkdir = await evaluate(
    `Array.from(document.querySelectorAll(".files-name")).map((n) => n.textContent)`,
  );
  results.mkdirError = await evaluate(`document.getElementById("files-error").hidden ? "" : document.getElementById("files-error").textContent`);
  if (!results.afterMkdir.some((t) => t.includes(folder))) {
    throw new Error(`folder not listed: ${JSON.stringify(results.afterMkdir)}`);
  }

  // Enter the folder, then upload a file into it via the file input.
  await evaluate(`(() => {
    const row = Array.from(document.querySelectorAll(".files-name")).find((n) => n.textContent.includes(${JSON.stringify(folder)}));
    row.click();
  })()`);
  await waitFor(
    `document.getElementById("files-path").textContent.endsWith(${JSON.stringify(folder)}) ? "ready" : ""`,
    "the folder navigation",
    20000,
  );
  results.insideFolder = await evaluate(`document.getElementById("files-path").textContent`);

  const local = path.join(workDir, "uploaded.txt");
  fs.writeFileSync(local, "uploaded from the browser smoke test\n");
  const { root } = await send("DOM.getDocument", {}, sessionId);
  const { nodeId } = await send(
    "DOM.querySelector",
    { nodeId: root.nodeId, selector: "#files-input" },
    sessionId,
  );
  await send("DOM.setFileInputFiles", { nodeId, files: [local] }, sessionId);
  await waitFor(
    `Array.from(document.querySelectorAll(".files-name")).some((n) => n.textContent.includes("uploaded.txt")) ? "ready" : ""`,
    "the uploaded file",
    20000,
  );
  results.afterUpload = await evaluate(
    `Array.from(document.querySelectorAll(".files-name")).map((n) => n.textContent)`,
  );
  if (!results.afterUpload.some((t) => t.includes("uploaded.txt"))) {
    throw new Error(`upload missing from listing: ${JSON.stringify(results.afterUpload)}`);
  }

  // Simulate a drag-and-drop of a new file into the listing.
  await evaluate(`(() => {
    const dt = new DataTransfer();
    dt.items.add(new File(["dropped from the smoke test"], "dropped.txt", { type: "text/plain" }));
    const list = document.getElementById("files-list");
    list.dispatchEvent(new DragEvent("dragover", { dataTransfer: dt, bubbles: true, cancelable: true }));
    list.dispatchEvent(new DragEvent("drop", { dataTransfer: dt, bubbles: true, cancelable: true }));
  })()`);
  await waitFor(
    `Array.from(document.querySelectorAll(".files-name")).some((n) => n.textContent.includes("dropped.txt")) ? "ready" : ""`,
    "the dropped file",
    20000,
  );
  results.afterDrop = await evaluate(
    `Array.from(document.querySelectorAll(".files-name")).map((n) => n.textContent)`,
  );
  if (!results.afterDrop.some((t) => t.includes("dropped.txt"))) {
    throw new Error(`drag-and-drop upload missing: ${JSON.stringify(results.afterDrop)}`);
  }

  // Download the file: intercept the request rather than writing to disk.
  results.downloadBody = await evaluate(`(async () => {
    const url = "/api/sessions/" + encodeURIComponent(${JSON.stringify(sessionID)}) +
      "/files/content?path=" + encodeURIComponent(${JSON.stringify(results.insideFolder + "/uploaded.txt")});
    const res = await fetch(url);
    return { status: res.status, text: await res.text(), disposition: res.headers.get("Content-Disposition") };
  })()`);
  if (results.downloadBody.status !== 200 || !results.downloadBody.text.includes("browser smoke")) {
    throw new Error(`download failed: ${JSON.stringify(results.downloadBody)}`);
  }

  // Delete the uploaded file (we are inside the folder), go Home, delete the
  // folder itself.
  await evaluate(`window.confirm = () => true`);
  async function deleteRow(name) {
    await waitFor(
      `Array.from(document.querySelectorAll(".files-name")).some((n) => n.textContent.includes(${JSON.stringify(name)})) ? "present" : ""`,
      `the row for ${name}`,
      20000,
    );
    await evaluate(`(() => {
      const rows = Array.from(document.querySelectorAll(".files-row"));
      const row = rows.find((r) => r.querySelector(".files-name").textContent.includes(${JSON.stringify(name)}));
      row.querySelector(".files-delete").click();
    })()`);
    await waitFor(
      `Array.from(document.querySelectorAll(".files-name")).some((n) => n.textContent.includes(${JSON.stringify(name)})) ? "" : "gone"`,
      `deleting ${name}`,
      20000,
    );
  }
  await deleteRow("dropped.txt");
  await deleteRow("uploaded.txt");
  results.afterDeleteFile = await evaluate(
    `Array.from(document.querySelectorAll(".files-name")).map((n) => n.textContent)`,
  );
  if (results.afterDeleteFile.some((t) => t.includes("uploaded.txt"))) {
    throw new Error(`file still listed after delete: ${JSON.stringify(results.afterDeleteFile)}`);
  }
  await evaluate(`document.getElementById("files-home").click()`);
  await sleep(800);
  await deleteRow(folder);
  results.finalListing = await evaluate(
    `Array.from(document.querySelectorAll(".files-name")).map((n) => n.textContent)`,
  );
  if (results.finalListing.some((t) => t.includes(folder))) {
    throw new Error(`folder still listed: ${JSON.stringify(results.finalListing)}`);
  }

  // Escape closes the panel.
  await evaluate(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await sleep(400);
  results.modalClosed = await evaluate(`document.getElementById("files-modal").hidden`);

  results.consoleErrors = consoleErrors;
  console.log(JSON.stringify(results, null, 2));

  ws.close();
  child.kill();
  await sleep(1200);
  try {
    fs.rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 300 });
  } catch (_) {
    // The temporary profile is left behind if Edge still holds a lock.
  }
  if (consoleErrors.length) {
    throw new Error(`browser console errors: ${consoleErrors.join(" | ")}`);
  }
}

main().catch((error) => {
  console.error("SMOKE FAILED:", error.message);
  console.error("PARTIAL:", JSON.stringify(globalThis.__partial || {}, null, 2));
  process.exit(1);
});
