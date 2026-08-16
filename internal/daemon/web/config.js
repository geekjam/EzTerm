(() => {
  "use strict";

  const elements = {
    form: document.getElementById("config-form"),
    list: document.getElementById("config-list"),
    count: document.getElementById("config-count"),
    status: document.getElementById("status"),
    heading: document.getElementById("editor-heading"),
    modeBadge: document.getElementById("form-mode"),
    localType: document.getElementById("local-type-button"),
    sshType: document.getElementById("ssh-type-button"),
    localFields: document.getElementById("local-fields"),
    sshFields: document.getElementById("ssh-fields"),
    passwordField: document.getElementById("password-field"),
    passwordHint: document.getElementById("password-hint"),
    keyField: document.getElementById("key-field"),
    cancel: document.getElementById("cancel-button"),
    refresh: document.getElementById("refresh-button"),
    save: document.getElementById("save-button"),
    name: document.getElementById("name"),
    command: document.getElementById("command"),
    args: document.getElementById("args"),
    mode: document.getElementById("mode"),
    host: document.getElementById("host"),
    port: document.getElementById("port"),
    user: document.getElementById("user"),
    authMethod: document.getElementById("auth-method"),
    password: document.getElementById("password"),
    keyPath: document.getElementById("key-path"),
    shell: document.getElementById("shell"),
  };

  let currentType = "local";
  let editingName = "";

  function setStatus(message, kind) {
    elements.status.textContent = message || "";
    elements.status.className = `status${kind ? ` ${kind}` : ""}`;
  }

  async function request(path, options) {
    const response = await fetch(path, {
      headers: { "Content-Type": "application/json" },
      ...options,
    });
    let body = null;
    try {
      body = await response.json();
    } catch (_) {
      // DELETE returns no body.
    }
    if (!response.ok) {
      throw new Error(body && body.error ? body.error : `Request failed (${response.status})`);
    }
    return body;
  }

  function escapeHTML(value) {
    return String(value == null ? "" : value)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#039;");
  }

  function configDescription(config) {
    if (config.type === "ssh") {
      const address = `${config.user ? `${config.user}@` : ""}${config.host || ""}:${config.port || 22}`;
      const auth = config.auth_method === "key" ? "key auth" : "password auth";
      return `${address} · ${auth}`;
    }
    const command = config.command || "default shell";
    const args = config.args && config.args.length ? ` ${config.args.join(" ")}` : "";
    return `${command}${args} · ${config.mode || "pty"}`;
  }

  function renderConfigs(configs) {
    elements.count.textContent = String(configs.length);
    if (!configs.length) {
      elements.list.innerHTML = '<div class="empty-state">No configurations yet. Create one on the right.</div>';
      return;
    }
    elements.list.innerHTML = configs.map((config) => `
      <article class="config-card">
        <div class="config-main">
          <div class="config-title">
            <span class="config-name">${escapeHTML(config.name)}</span>
            <span class="type-label ${escapeHTML(config.type)}">${escapeHTML(config.type)}</span>
          </div>
          <div class="config-detail" title="${escapeHTML(configDescription(config))}">${escapeHTML(configDescription(config))}</div>
        </div>
        <div class="config-actions">
          <button class="button button-quiet edit-button" type="button" data-name="${escapeHTML(config.name)}">Edit</button>
          <button class="button button-danger delete-button" type="button" data-name="${escapeHTML(config.name)}">Delete</button>
        </div>
      </article>
    `).join("");

    elements.list.querySelectorAll(".edit-button").forEach((button) => {
      button.addEventListener("click", () => editConfig(button.dataset.name));
    });
    elements.list.querySelectorAll(".delete-button").forEach((button) => {
      button.addEventListener("click", () => deleteConfig(button.dataset.name));
    });
  }

  async function loadConfigs(showMessage) {
    if (showMessage) {
      setStatus("Loading configurations...");
    }
    try {
      const body = await request("/api/configs");
      renderConfigs(body.configs || []);
      if (showMessage) {
        setStatus("Configurations refreshed.", "success");
      }
    } catch (error) {
      elements.list.innerHTML = `<div class="empty-state">${escapeHTML(error.message)}</div>`;
      setStatus(error.message, "error");
    }
  }

  function setType(type) {
    currentType = type;
    const local = type === "local";
    elements.localType.classList.toggle("active", local);
    elements.sshType.classList.toggle("active", !local);
    elements.localFields.classList.toggle("hidden", !local);
    elements.sshFields.classList.toggle("hidden", local);
    elements.modeBadge.textContent = type.toUpperCase();
    elements.modeBadge.className = `mode-badge ${local ? "" : "ssh"}`;
    updateAuthFields();
  }

  function updateAuthFields() {
    const password = elements.authMethod.value === "password";
    elements.passwordField.classList.toggle("hidden", !password);
    elements.keyField.classList.toggle("hidden", password);
  }

  function clearForm() {
    editingName = "";
    elements.form.reset();
    elements.port.value = "22";
    elements.mode.value = "pty";
    elements.authMethod.value = "password";
    elements.heading.textContent = "New configuration";
    elements.save.textContent = "Save configuration";
    elements.cancel.classList.add("hidden");
    elements.name.disabled = false;
    elements.password.placeholder = "Enter to set or replace";
    elements.passwordHint.textContent = "The stored password is never sent back to this page. Leave blank to keep it.";
    setType("local");
    setStatus("");
  }

  async function editConfig(name) {
    setStatus(`Loading ${name}...`);
    try {
      const config = await request(`/api/configs/${encodeURIComponent(name)}`);
      editingName = config.name;
      elements.name.value = config.name || "";
      elements.name.disabled = true;
      elements.heading.textContent = `Edit ${config.name}`;
      elements.save.textContent = "Save changes";
      elements.cancel.classList.remove("hidden");
      setType(config.type);

      if (config.type === "local") {
        elements.command.value = config.command || "";
        elements.args.value = (config.args || []).join("\n");
        elements.mode.value = config.mode || "pty";
      } else {
        elements.host.value = config.host || "";
        elements.port.value = config.port || 22;
        elements.user.value = config.user || "";
        elements.authMethod.value = config.auth_method || "password";
        elements.keyPath.value = config.key_path || "";
        elements.shell.value = config.default_shell || "";
        elements.password.value = "";
        elements.password.placeholder = config.auth_method === "password" ? "Leave blank to keep stored password" : "Enter password to switch auth";
        elements.passwordHint.textContent = config.auth_method === "password"
          ? "A password is already stored but is never returned. Leave blank to keep it."
          : "Passwords are write-only and are not returned by the API.";
        updateAuthFields();
      }
      setStatus(`Editing ${name}.`, "success");
      elements.name.focus();
    } catch (error) {
      setStatus(error.message, "error");
    }
  }

  function formArgs() {
    return elements.args.value.split(/\r?\n/).map((value) => value.trim()).filter(Boolean);
  }

  function formPayload() {
    if (currentType === "local") {
      return {
        type: "local",
        command: elements.command.value,
        args: formArgs(),
        mode: elements.mode.value,
      };
    }
    return {
      type: "ssh",
      host: elements.host.value,
      port: Number(elements.port.value || 22),
      user: elements.user.value,
      auth_method: elements.authMethod.value,
      password: elements.password.value,
      key_path: elements.keyPath.value,
      shell: elements.shell.value,
    };
  }

  async function saveConfig(event) {
    event.preventDefault();
    if (!elements.form.reportValidity()) {
      return;
    }
    const name = editingName || elements.name.value.trim();
    if (!name) {
      setStatus("Name is required.", "error");
      elements.name.focus();
      return;
    }
    elements.save.disabled = true;
    setStatus("Saving configuration...");
    try {
      await request(`/api/configs/${encodeURIComponent(name)}`, {
        method: "POST",
        body: JSON.stringify(formPayload()),
      });
      await loadConfigs(false);
      setStatus(`Configuration ${name} saved.`, "success");
      clearForm();
    } catch (error) {
      setStatus(error.message, "error");
    } finally {
      elements.save.disabled = false;
    }
  }

  async function deleteConfig(name) {
    if (!window.confirm(`Delete configuration "${name}"?`)) {
      return;
    }
    setStatus(`Deleting ${name}...`);
    try {
      await request(`/api/configs/${encodeURIComponent(name)}`, { method: "DELETE" });
      if (editingName === name) {
        clearForm();
      }
      await loadConfigs(false);
      setStatus(`Configuration ${name} deleted.`, "success");
    } catch (error) {
      setStatus(error.message, "error");
    }
  }

  elements.localType.addEventListener("click", () => setType("local"));
  elements.sshType.addEventListener("click", () => setType("ssh"));
  elements.authMethod.addEventListener("change", updateAuthFields);
  elements.form.addEventListener("submit", saveConfig);
  elements.cancel.addEventListener("click", clearForm);
  elements.refresh.addEventListener("click", () => loadConfigs(true));

  clearForm();
  loadConfigs(false);
})();
