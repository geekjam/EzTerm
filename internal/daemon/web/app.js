(() => {
  "use strict";

  const terminalElement = document.getElementById("terminal");
  const statusElement = document.getElementById("status");
  const modalElement = document.getElementById("modal");
  const modalTitleElement = document.getElementById("modal-title");
  const modalMessageElement = document.getElementById("modal-message");
  const modalConfirmElement = document.getElementById("modal-confirm");
  const modalCancelElement = document.getElementById("modal-cancel");
  const modalSendElement = document.getElementById("modal-send");
  const passwordFormElement = document.getElementById("password-form");
  const passwordInputElement = document.getElementById("password-input");
  const passwordErrorElement = document.getElementById("password-error");
  const filesOpenButton = document.getElementById("files-open");
  const filesModalElement = document.getElementById("files-modal");
  const filesPathElement = document.getElementById("files-path");
  const filesListElement = document.getElementById("files-list");
  const filesHomeElement = document.getElementById("files-home");
  const filesUpElement = document.getElementById("files-up");
  const filesUploadElement = document.getElementById("files-upload");
  const filesInputElement = document.getElementById("files-input");
  const filesNewdirElement = document.getElementById("files-newdir");
  const filesNewdirFormElement = document.getElementById("files-newdir-form");
  const filesNewdirNameElement = document.getElementById("files-newdir-name");
  const filesNewdirCreateElement = document.getElementById("files-newdir-create");
  const filesNewdirCancelElement = document.getElementById("files-newdir-cancel");
  const filesProgressElement = document.getElementById("files-progress");
  const filesProgressFillElement = document.getElementById("files-progress-fill");
  const filesProgressTextElement = document.getElementById("files-progress-text");
  const filesProgressCancelElement = document.getElementById("files-progress-cancel");
  const filesErrorElement = document.getElementById("files-error");
  const filesCloseElement = document.getElementById("files-close");
  const sessionID = window.location.pathname.split("/")[2];
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  const socketURL = `${protocol}//${window.location.host}/web/${encodeURIComponent(sessionID)}/ws`;

  // Dialog copy. A session that ended keeps its terminal frozen; a dropped
  // WebSocket keeps reconnecting in the background.
  const SESSION_END_DIALOG = {
    title: "Session disconnected",
    message: "The user session has ended.",
  };
  const SOCKET_DOWN_DIALOG = {
    title: "Connection lost",
    message: "The WebSocket connection was lost. Reconnecting…",
  };
  const PASSWORD_DIALOG = {
    ssh_password: {
      title: "SSH password required",
      message: "This session connects after the password is submitted.",
    },
    terminal_password: {
      title: "Password required",
      message: "The session is waiting for a password.",
    },
  };
  const PASSWORD_PENDING_STATUS = "password required — click the terminal to enter it";

  let socket;
  let terminal;
  let fitAddon;
  let reconnectTimer;
  let intentionalClose = false;
  let resizing = false;
  let sessionEnded = false;
  let modalOpen = false;
  let activePrompt = null; // prompt announced by the server and still pending
  let passwordDialog = false; // the modal currently shows the password form
  let sessionIsRemote = false; // SSH sessions do not follow symlinks
  let filesOpen = false; // the file browser modal is showing
  let filesBusy = false; // an upload sequence is in flight
  let filesPath = "";
  let filesRoot = "";
  let filesParent = "";
  let filesUploadXHR = null;
  let filesUploadCancelled = false;
  let filesRefreshTimer = null;

  function setStatus(text, connected) {
    statusElement.textContent = text;
    statusElement.dataset.connected = connected ? "true" : "false";
  }

  function clearPasswordFields() {
    passwordDialog = false;
    passwordFormElement.hidden = true;
    passwordInputElement.value = "";
    passwordErrorElement.textContent = "";
    passwordErrorElement.hidden = true;
    modalCancelElement.hidden = true;
    modalSendElement.hidden = true;
    modalConfirmElement.hidden = false;
    modalConfirmElement.textContent = "OK";
  }

  function showModal(dialog) {
    clearPasswordFields();
    modalTitleElement.textContent = dialog.title;
    modalMessageElement.textContent = dialog.message;
    modalElement.hidden = false;
    modalOpen = true;
    modalConfirmElement.focus();
  }

  function hideModal() {
    activePrompt = null;
    clearPasswordFields();
    modalElement.hidden = true;
    modalOpen = false;
    if (terminal) {
      terminal.focus();
    }
  }

  function showPasswordDialog(prompt) {
    activePrompt = prompt;
    passwordDialog = true;
    const copy = PASSWORD_DIALOG[prompt.kind] || PASSWORD_DIALOG.terminal_password;
    modalTitleElement.textContent = copy.title;
    modalMessageElement.textContent = prompt.label || copy.message;
    passwordFormElement.hidden = false;
    passwordInputElement.value = "";
    passwordErrorElement.textContent = "";
    passwordErrorElement.hidden = true;
    modalCancelElement.hidden = false;
    modalSendElement.hidden = false;
    modalConfirmElement.hidden = true;
    modalElement.hidden = false;
    modalOpen = true;
    passwordInputElement.focus();
  }

  function cancelPasswordDialog() {
    // Cancelling only dismisses the dialog locally; the server still waits for
    // the secret, so clicking the terminal brings the form back.
    clearPasswordFields();
    modalElement.hidden = true;
    modalOpen = false;
    if (activePrompt) {
      setStatus(PASSWORD_PENDING_STATUS, false);
    }
    if (terminal) {
      terminal.focus();
    }
  }

  function showPasswordError(message) {
    passwordErrorElement.textContent = message;
    passwordErrorElement.hidden = false;
    passwordInputElement.focus();
  }

  function submitPassword() {
    if (!passwordDialog || !activePrompt) {
      return;
    }
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      showPasswordError("Connection is not open. Reconnecting…");
      return;
    }
    if (!passwordInputElement.value) {
      passwordInputElement.focus();
      return;
    }
    socket.send(JSON.stringify({ type: "secret", text: passwordInputElement.value }));
    // Clear the field immediately and never keep the secret in page state.
    passwordInputElement.value = "";
    passwordErrorElement.textContent = "";
    passwordErrorElement.hidden = true;
    setStatus("password submitted", true);
  }

  function sendResize() {
    if (!socket || socket.readyState !== WebSocket.OPEN || !terminal) {
      return;
    }
    fitAddon.fit();
    socket.send(JSON.stringify({
      type: "resize",
      rows: terminal.rows,
      cols: terminal.cols,
    }));
  }

  // --- file browser -------------------------------------------------------

  function sessionFilesURL() {
    return `/api/sessions/${encodeURIComponent(sessionID)}/files`;
  }

  async function readJSON(response) {
    try {
      return await response.json();
    } catch (_) {
      return {};
    }
  }

  function showFilesError(message) {
    filesErrorElement.textContent = message || "";
    filesErrorElement.hidden = !message;
  }

  // refreshSessionInfo decides whether file transfer is available: the daemon
  // serves it only for running sessions. It is also how the page learns
  // whether symlinks are followed (local) or rejected (SSH).
  async function refreshSessionInfo() {
    try {
      const response = await fetch(`/api/sessions/${encodeURIComponent(sessionID)}`);
      if (!response.ok) {
        filesOpenButton.hidden = true;
        return;
      }
      const info = await response.json();
      sessionIsRemote = Boolean(info.ssh_config && info.ssh_config !== "internal");
      const canTransfer = info.status === "running";
      filesOpenButton.hidden = !canTransfer;
      if (!canTransfer && filesOpen) {
        closeFilesDialog();
      }
      // A session that is still dialing (or waiting for a password) becomes
      // running later; poll just those two states until the button can show.
      if (!canTransfer && !sessionEnded && (info.status === "starting" || info.status === "awaiting_password")) {
        scheduleFilesRefresh();
      }
    } catch (_) {
      // A reconnect (or the next prompt) retries.
    }
  }

  function scheduleFilesRefresh() {
    if (filesRefreshTimer || sessionEnded) {
      return;
    }
    filesRefreshTimer = window.setTimeout(() => {
      filesRefreshTimer = null;
      if (!sessionEnded) {
        refreshSessionInfo();
      }
    }, 1000);
  }

  function openFilesDialog() {
    if (filesOpen) {
      return;
    }
    filesOpen = true;
    filesModalElement.hidden = false;
    filesNewdirFormElement.hidden = true;
    filesProgressElement.hidden = true;
    showFilesError("");
    loadFiles("");
    filesCloseElement.focus();
  }

  function closeFilesDialog() {
    if (!filesOpen) {
      return;
    }
    if (filesUploadXHR) {
      filesUploadCancelled = true;
      try {
        filesUploadXHR.abort();
      } catch (_) {
        // already finished
      }
      filesUploadXHR = null;
    }
    filesOpen = false;
    filesBusy = false;
    filesModalElement.hidden = true;
    filesListElement.classList.remove("files-dragover");
    if (terminal && !modalOpen) {
      terminal.focus();
    }
  }

  async function loadFiles(path) {
    showFilesError("");
    filesListElement.classList.remove("files-dragover");
    filesListElement.textContent = "";
    const loading = document.createElement("p");
    loading.className = "files-empty";
    loading.textContent = "Loading…";
    filesListElement.append(loading);
    try {
      const response = await fetch(`${sessionFilesURL()}?path=${encodeURIComponent(path)}`);
      const data = await readJSON(response);
      if (!response.ok) {
        throw new Error(data.error || `HTTP ${response.status}`);
      }
      filesPath = data.path || "";
      filesRoot = data.root || "";
      filesParent = data.parent || "";
      filesPathElement.textContent = filesPath || filesRoot;
      filesPathElement.title = filesPath || filesRoot;
      filesUpElement.disabled = !filesParent;
      renderFiles(data.entries || []);
    } catch (error) {
      filesListElement.textContent = "";
      showFilesError(error.message || "cannot list files");
    }
  }

  function renderFiles(entries) {
    filesListElement.textContent = "";
    if (!entries.length) {
      const empty = document.createElement("p");
      empty.className = "files-empty";
      empty.textContent = "This folder is empty.";
      filesListElement.append(empty);
      return;
    }
    for (const entry of entries) {
      const row = document.createElement("div");
      row.className = "files-row";
      if (entry.dir) {
        row.classList.add("files-row-dir");
      }

      const name = document.createElement("button");
      name.type = "button";
      name.className = "files-name";
      const marker = entry.dir ? "\u25b8 " : entry.symlink ? "\u2197 " : "\u00b7 ";
      name.textContent = marker + entry.name;
      if (entry.symlink) {
        const badge = document.createElement("span");
        badge.className = "files-badge";
        badge.textContent = "link";
        name.append(badge);
      }
      // SSH sessions never follow a symlink; the server rejects such reads.
      const navigable = entry.dir || !(entry.symlink && sessionIsRemote);
      if (!navigable) {
        name.disabled = true;
        name.title = "Symbolic links are not followed for SSH sessions";
      } else {
        name.title = entry.dir ? `Open ${entry.name}` : `Download ${entry.name}`;
        name.addEventListener("click", () => {
          if (entry.dir) {
            loadFiles(entry.path);
          } else {
            downloadFile(entry.path);
          }
        });
      }

      const size = document.createElement("span");
      size.className = "files-size";
      size.textContent = entry.dir ? "-" : formatSize(entry.size);

      const time = document.createElement("span");
      time.className = "files-time";
      time.textContent = formatTime(entry.modified);

      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "files-delete";
      remove.textContent = "Delete";
      remove.addEventListener("click", () => deleteEntry(entry));

      row.append(name, size, time, remove);
      filesListElement.append(row);
    }
  }

  function formatSize(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) {
      return "-";
    }
    if (bytes < 1024) {
      return `${bytes} B`;
    }
    const units = ["KiB", "MiB", "GiB", "TiB"];
    let value = bytes / 1024;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit += 1;
    }
    return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
  }

  function formatTime(value) {
    if (!value) {
      return "-";
    }
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
  }

  function joinPath(dir, name) {
    if (!dir) {
      return name;
    }
    const separator = dir.includes("\\") && !dir.includes("/") ? "\\" : "/";
    if (dir.endsWith("/") || dir.endsWith("\\")) {
      return dir + name;
    }
    return dir + separator + name;
  }

  function downloadFile(path) {
    // Content-Disposition: attachment keeps the browser from navigating away
    // from the terminal page.
    const link = document.createElement("a");
    link.href = `${sessionFilesURL()}/content?path=${encodeURIComponent(path)}`;
    link.download = "";
    link.rel = "noopener";
    document.body.append(link);
    link.click();
    link.remove();
  }

  async function deleteEntry(entry) {
    const kind = entry.dir ? "folder" : "file";
    if (!window.confirm(`Delete ${kind} "${entry.name}"?`)) {
      return;
    }
    try {
      const response = await fetch(`${sessionFilesURL()}?path=${encodeURIComponent(entry.path)}`, {
        method: "DELETE",
      });
      if (!response.ok) {
        const data = await readJSON(response);
        throw new Error(data.error || `HTTP ${response.status}`);
      }
      await loadFiles(filesPath);
    } catch (error) {
      showFilesError(error.message || "cannot delete");
    }
  }

  async function uploadFiles(fileList) {
    const files = Array.from(fileList || []);
    if (!files.length || filesBusy) {
      return;
    }
    filesBusy = true;
    filesUploadCancelled = false;
    showFilesError("");
    filesProgressElement.hidden = false;
    filesProgressFillElement.style.width = "0%";
    filesProgressTextElement.textContent = "";
    try {
      for (let index = 0; index < files.length; index += 1) {
        if (filesUploadCancelled) {
          break;
        }
        await uploadOne(files[index], joinPath(filesPath, files[index].name), index, files.length);
      }
    } catch (error) {
      if (!filesUploadCancelled) {
        showFilesError(error.message || "upload failed");
      }
    } finally {
      filesUploadXHR = null;
      filesBusy = false;
      filesProgressElement.hidden = true;
      if (!filesUploadCancelled) {
        await loadFiles(filesPath);
      }
    }
  }

  function uploadOne(file, target, index, total) {
    return new Promise((resolve, reject) => {
      const request = new XMLHttpRequest();
      filesUploadXHR = request;
      request.open(
        "POST",
        `${sessionFilesURL()}/content?path=${encodeURIComponent(target)}&overwrite=true`,
      );
      request.setRequestHeader("Content-Type", "application/octet-stream");
      request.upload.addEventListener("progress", (event) => {
        const label = total > 1 ? `${index + 1}/${total} ${file.name}` : file.name;
        if (event.lengthComputable) {
          const percent = Math.round((event.loaded / event.total) * 100);
          filesProgressFillElement.style.width = `${percent}%`;
          filesProgressTextElement.textContent = `Uploading ${label} \u2014 ${percent}%`;
        } else {
          filesProgressTextElement.textContent = `Uploading ${label} \u2014 ${formatSize(event.loaded)}`;
        }
      });
      request.addEventListener("load", () => {
        if (request.status >= 200 && request.status < 300) {
          filesProgressFillElement.style.width = "100%";
          resolve();
          return;
        }
        let message = `HTTP ${request.status}`;
        try {
          const data = JSON.parse(request.responseText);
          if (data && data.error) {
            message = data.error;
          }
        } catch (_) {
          // keep the status message
        }
        reject(new Error(message));
      });
      request.addEventListener("error", () => reject(new Error("upload failed")));
      request.addEventListener("abort", () => reject(new Error("upload cancelled")));
      request.send(file);
    });
  }

  function showNewDirForm() {
    filesNewdirFormElement.hidden = false;
    filesNewdirNameElement.value = "";
    filesNewdirNameElement.focus();
  }

  async function createNewDir() {
    const name = filesNewdirNameElement.value.trim();
    if (!name) {
      filesNewdirNameElement.focus();
      return;
    }
    try {
      const response = await fetch(sessionFilesURL(), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: joinPath(filesPath, name) }),
      });
      if (!response.ok) {
        const data = await readJSON(response);
        throw new Error(data.error || `HTTP ${response.status}`);
      }
      filesNewdirFormElement.hidden = true;
      await loadFiles(filesPath);
    } catch (error) {
      showFilesError(error.message || "cannot create folder");
    }
  }

  function hasFiles(event) {
    const types = event.dataTransfer ? Array.from(event.dataTransfer.types || []) : [];
    return types.includes("Files");
  }

  function connect() {
    if (intentionalClose || sessionEnded) {
      return;
    }
    setStatus("connecting...", false);
    socket = new WebSocket(socketURL);
    socket.binaryType = "arraybuffer";

    socket.addEventListener("open", () => {
      // A successful reconnect restores the terminal and clears the transport
      // dialog. A password dialog may be replayed right after this event.
      if (modalOpen && !passwordDialog) {
        hideModal();
      }
      setStatus("connected", true);
      refreshSessionInfo();
      sendResize();
      if (passwordDialog) {
        passwordInputElement.focus();
      } else {
        terminal.focus();
      }
    });

    socket.addEventListener("message", (event) => {
      if (event.data instanceof ArrayBuffer) {
        terminal.write(new Uint8Array(event.data));
        return;
      }
      if (typeof event.data !== "string") {
        return;
      }
      let message;
      try {
        message = JSON.parse(event.data);
      } catch (_) {
        // Ignore non-protocol text frames.
        return;
      }
      if (message.type === "error") {
        setStatus(message.error || "connection error", false);
        return;
      }
      if (message.type === "prompt") {
        if (filesOpen) {
          closeFilesDialog();
        }
        showPasswordDialog({ kind: message.kind, label: message.label });
        return;
      }
      if (message.type === "prompt-cleared") {
        activePrompt = null;
        if (passwordDialog) {
          hideModal();
          setStatus("connected", true);
        } else if (!modalOpen) {
          setStatus("connected", socket && socket.readyState === WebSocket.OPEN);
        }
        return;
      }
      if (message.type === "secret-result") {
        if (message.ok) {
          // The prompt is cleared by the server once it is consumed; keep the
          // dialog until then in case the program rejects the answer.
          setStatus("password submitted", true);
          refreshSessionInfo();
          return;
        }
        showPasswordError(message.error || "the password was rejected");
        return;
      }
      if (message.type === "session-end") {
        // The process exited: this is not a transport problem, so stop
        // reconnecting and tell the user the session is gone.
        sessionEnded = true;
        clearTimeout(reconnectTimer);
        setStatus("session ended", false);
        activePrompt = null;
        filesOpenButton.hidden = true;
        if (filesRefreshTimer) {
          clearTimeout(filesRefreshTimer);
          filesRefreshTimer = null;
        }
        closeFilesDialog();
        showModal(SESSION_END_DIALOG);
      }
    });

    socket.addEventListener("close", () => {
      if (intentionalClose) {
        setStatus("disconnected", false);
        return;
      }
      if (sessionEnded) {
        // The close belongs to the session-end marker already handled above.
        setStatus("session ended", false);
        return;
      }
      setStatus("disconnected", false);
      showModal(SOCKET_DOWN_DIALOG);
      clearTimeout(reconnectTimer);
      reconnectTimer = setTimeout(connect, 1000);
    });

    socket.addEventListener("error", () => {
      if (sessionEnded || intentionalClose) {
        return;
      }
      setStatus("connection error", false);
      showModal(SOCKET_DOWN_DIALOG);
    });
  }

  function resizeTerminal() {
    if (fitAddon) {
      sendResize();
    }
  }

  function boot() {
    if (!window.Terminal || !window.FitAddon) {
      setStatus("terminal assets unavailable", false);
      return;
    }

    terminal = new window.Terminal({
      cursorBlink: true,
      convertEol: false,
      fontFamily: "ui-monospace, SFMono-Regular, Consolas, monospace",
      fontSize: 14,
      scrollback: 5000,
      theme: {
        background: "#111417",
        foreground: "#e7edf2",
        cursor: "#f0b35b",
        selectionBackground: "#35536b",
      },
    });
    fitAddon = new window.FitAddon.FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(terminalElement);
    fitAddon.fit();

    terminal.onData((data) => {
      // The dialog is modal: keys typed while it is open never reach the
      // session, and an ended session stays frozen. A dismissed password
      // dialog keeps the prompt active, so typing is blocked until the user
      // answers it (or the program moves on).
      if (modalOpen || filesOpen || sessionEnded || activePrompt) {
        return;
      }
      if (socket && socket.readyState === WebSocket.OPEN) {
        socket.send(new TextEncoder().encode(data));
      }
    });
    // fit() inside sendResize can re-trigger onResize, so guard against
    // infinite resize loops before notifying the server.
    terminal.onResize(() => {
      if (resizing) {
        return;
      }
      resizing = true;
      try {
        sendResize();
      } finally {
        resizing = false;
      }
    });
    window.addEventListener("resize", resizeTerminal);
    terminalElement.addEventListener("click", () => {
      if (filesOpen || passwordDialog) {
        return;
      }
      if (activePrompt) {
        // The user dismissed the dialog with Cancel; let a click reopen it.
        showPasswordDialog(activePrompt);
        return;
      }
      if (!modalOpen) {
        terminal.focus();
      }
    });

    passwordFormElement.addEventListener("submit", (event) => {
      event.preventDefault();
      submitPassword();
    });
    modalConfirmElement.addEventListener("click", () => {
      // Confirming only dismisses the dialog: an ended session stays ended and
      // a dropped connection keeps reconnecting in the background.
      hideModal();
    });
    modalCancelElement.addEventListener("click", () => {
      cancelPasswordDialog();
    });
    modalElement.addEventListener("click", (event) => {
      if (event.target !== modalElement) {
        return;
      }
      if (passwordDialog) {
        cancelPasswordDialog();
      } else {
        hideModal();
      }
    });
    filesOpenButton.addEventListener("click", openFilesDialog);
    filesCloseElement.addEventListener("click", closeFilesDialog);
    filesHomeElement.addEventListener("click", () => loadFiles(""));
    filesUpElement.addEventListener("click", () => {
      if (filesParent) {
        loadFiles(filesParent);
      }
    });
    filesUploadElement.addEventListener("click", () => filesInputElement.click());
    filesInputElement.addEventListener("change", () => {
      uploadFiles(filesInputElement.files);
      filesInputElement.value = "";
    });
    filesNewdirElement.addEventListener("click", showNewDirForm);
    filesNewdirCreateElement.addEventListener("click", createNewDir);
    filesNewdirCancelElement.addEventListener("click", () => {
      filesNewdirFormElement.hidden = true;
    });
    filesNewdirNameElement.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        createNewDir();
      } else if (event.key === "Escape") {
        event.preventDefault();
        filesNewdirFormElement.hidden = true;
      }
    });
    filesProgressCancelElement.addEventListener("click", () => {
      filesUploadCancelled = true;
      if (filesUploadXHR) {
        try {
          filesUploadXHR.abort();
        } catch (_) {
          // already finished
        }
      }
    });
    filesModalElement.addEventListener("click", (event) => {
      if (event.target === filesModalElement) {
        closeFilesDialog();
      }
    });
    filesListElement.addEventListener("dragover", (event) => {
      if (!hasFiles(event)) {
        return;
      }
      event.preventDefault();
      filesListElement.classList.add("files-dragover");
    });
    filesListElement.addEventListener("dragleave", () => {
      filesListElement.classList.remove("files-dragover");
    });
    filesListElement.addEventListener("drop", (event) => {
      if (!hasFiles(event)) {
        return;
      }
      event.preventDefault();
      filesListElement.classList.remove("files-dragover");
      uploadFiles(event.dataTransfer.files);
    });
    window.addEventListener("dragover", (event) => {
      if (hasFiles(event)) {
        event.preventDefault();
      }
    });
    window.addEventListener("drop", (event) => {
      // Keep a misplaced file drop from navigating the browser away from the
      // terminal; text drops still reach xterm.
      if (hasFiles(event)) {
        event.preventDefault();
      }
    });

    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape") {
        return;
      }
      if (filesOpen) {
        event.preventDefault();
        closeFilesDialog();
        return;
      }
      if (passwordDialog) {
        event.preventDefault();
        cancelPasswordDialog();
      }
    });

    refreshSessionInfo();
    connect();
  }

  window.addEventListener("beforeunload", () => {
    intentionalClose = true;
    clearTimeout(reconnectTimer);
    if (socket) {
      socket.close(1000, "page closed");
    }
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot, { once: true });
  } else {
    boot();
  }
})();
