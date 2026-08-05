(() => {
  "use strict";

  const terminalElement = document.getElementById("terminal");
  const statusElement = document.getElementById("status");
  const sessionID = window.location.pathname.split("/")[2];
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  const socketURL = `${protocol}//${window.location.host}/web/${encodeURIComponent(sessionID)}/ws`;

  let socket;
  let terminal;
  let fitAddon;
  let reconnectTimer;
  let intentionalClose = false;
  let resizing = false;

  function setStatus(text, connected) {
    statusElement.textContent = text;
    statusElement.dataset.connected = connected ? "true" : "false";
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
    if (intentionalClose) {
      return;
    }
    setStatus("connecting...", false);
    socket = new WebSocket(socketURL);
    socket.binaryType = "arraybuffer";

    socket.addEventListener("open", () => {
      setStatus("connected", true);
      sendResize();
      terminal.focus();
    });

    socket.addEventListener("message", (event) => {
      if (event.data instanceof ArrayBuffer) {
        terminal.write(new Uint8Array(event.data));
        return;
      }
      if (typeof event.data === "string") {
        try {
          const message = JSON.parse(event.data);
          if (message.type === "error") {
            setStatus(message.error || "connection error", false);
          }
        } catch (_) {
          // Ignore non-protocol text frames.
        }
      }
    });

    socket.addEventListener("close", () => {
      setStatus("disconnected", false);
      if (!intentionalClose) {
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(connect, 1000);
      }
    });

    socket.addEventListener("error", () => {
      setStatus("connection error", false);
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
    terminalElement.addEventListener("click", () => terminal.focus());
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