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
      if (modalOpen || sessionEnded || activePrompt) {
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
      if (passwordDialog) {
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
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && passwordDialog) {
        event.preventDefault();
        cancelPasswordDialog();
      }
    });

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
