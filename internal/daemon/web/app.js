(() => {
  "use strict";

  const terminalElement = document.getElementById("terminal");
  const statusElement = document.getElementById("status");
  const modalElement = document.getElementById("modal");
  const modalTitleElement = document.getElementById("modal-title");
  const modalMessageElement = document.getElementById("modal-message");
  const modalConfirmElement = document.getElementById("modal-confirm");
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

  let socket;
  let terminal;
  let fitAddon;
  let reconnectTimer;
  let intentionalClose = false;
  let resizing = false;
  let sessionEnded = false;
  let modalOpen = false;

  function setStatus(text, connected) {
    statusElement.textContent = text;
    statusElement.dataset.connected = connected ? "true" : "false";
  }

  function showModal(dialog) {
    modalTitleElement.textContent = dialog.title;
    modalMessageElement.textContent = dialog.message;
    modalElement.hidden = false;
    modalOpen = true;
    modalConfirmElement.focus();
  }

  function hideModal() {
    modalElement.hidden = true;
    modalOpen = false;
    if (terminal) {
      terminal.focus();
    }
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
      // A successful reconnect restores the terminal and clears the dialog.
      if (modalOpen) {
        hideModal();
      }
      setStatus("connected", true);
      sendResize();
      terminal.focus();
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
      if (message.type === "session-end") {
        // The process exited: this is not a transport problem, so stop
        // reconnecting and tell the user the session is gone.
        sessionEnded = true;
        clearTimeout(reconnectTimer);
        setStatus("session ended", false);
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
      // session, and an ended session stays frozen.
      if (modalOpen || sessionEnded) {
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
      if (!modalOpen) {
        terminal.focus();
      }
    });

    modalConfirmElement.addEventListener("click", () => {
      // Confirming only dismisses the dialog: an ended session stays ended and
      // a dropped connection keeps reconnecting in the background.
      hideModal();
    });
    modalElement.addEventListener("click", (event) => {
      if (event.target === modalElement) {
        hideModal();
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
