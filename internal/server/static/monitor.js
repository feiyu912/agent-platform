(function () {
  "use strict";

  var ENDPOINTS = {
    overview: "/api/monitor?messageLimit=50",
    connections: "/api/monitor/ws/connections?limit=100",
    messages: "/api/monitor/ws/messages?limit=50",
  };
  var TOKEN_STORAGE_KEY = "agent-platform.monitor.accessToken";

  var state = {
    loading: false,
    accessToken: "",
    selectedSessionId: "",
    knownSessionIds: [],
    overview: null,
    connectionsSnapshot: null,
    messagesSnapshot: null,
  };

  var dom = {};

  document.addEventListener("DOMContentLoaded", function () {
    dom = {
      sessionSelect: document.getElementById("session-select"),
      tokenInput: document.getElementById("token-input"),
      applyToken: document.getElementById("apply-token"),
      clearToken: document.getElementById("clear-token"),
      clearFilter: document.getElementById("clear-filter"),
      refreshButton: document.getElementById("refresh-button"),
      pageSubtitle: document.getElementById("page-subtitle"),
      loadingState: document.getElementById("loading-state"),
      errorBanner: document.getElementById("error-banner"),
      generatedAt: document.getElementById("generated-at"),
      overviewGrid: document.getElementById("overview-grid"),
      connectionsCount: document.getElementById("connections-count"),
      connectionsBody: document.getElementById("connections-body"),
      connectionsEmpty: document.getElementById("connections-empty"),
      messagesCount: document.getElementById("messages-count"),
      messagesBody: document.getElementById("messages-body"),
      messagesEmpty: document.getElementById("messages-empty"),
    };

    state.accessToken = loadStoredToken();
    dom.tokenInput.value = state.accessToken;
    dom.applyToken.addEventListener("click", function () {
      applyTokenFromInput();
    });
    dom.clearToken.addEventListener("click", function () {
      state.accessToken = "";
      dom.tokenInput.value = "";
      storeToken("");
      loadDashboard();
    });
    dom.tokenInput.addEventListener("keydown", function (event) {
      if (event.key === "Enter") {
        applyTokenFromInput();
      }
    });
    dom.refreshButton.addEventListener("click", function () {
      loadDashboard();
    });
    dom.clearFilter.addEventListener("click", function () {
      state.selectedSessionId = "";
      dom.sessionSelect.value = "";
      loadDashboard();
    });
    dom.sessionSelect.addEventListener("change", function (event) {
      state.selectedSessionId = event.target.value;
      loadDashboard();
    });

    setText(dom.pageSubtitle, window.location.host + " /monitor");
    renderEmptyShell();
    loadDashboard();
  });

  async function requestJSON(url) {
    var response;
    var headers = { Accept: "application/json" };
    var token = normalizedAccessToken(state.accessToken);
    if (token) {
      headers.Authorization = "Bearer " + token;
    }
    try {
      response = await fetch(url, {
        headers: headers,
        cache: "no-store",
      });
    } catch (error) {
      throw new Error("请求失败：" + readableError(error));
    }

    var envelope;
    try {
      envelope = await response.json();
    } catch (error) {
      throw new Error("接口返回了无法解析的 JSON：" + url);
    }

    if (!response.ok) {
      var httpMessage = envelope && envelope.msg ? envelope.msg : response.statusText;
      if (response.status === 401) {
        httpMessage = "Unauthorized。请在顶部输入 access_token 后点击“应用 token”。";
      }
      throw new Error("HTTP " + response.status + "：" + httpMessage);
    }
    if (!envelope || typeof envelope !== "object" || !Object.prototype.hasOwnProperty.call(envelope, "code")) {
      throw new Error("接口返回格式不符合 { code, msg, data }：" + url);
    }
    if (envelope.code !== 0) {
      throw new Error(envelope.msg || "接口返回 code=" + envelope.code);
    }
    return envelope.data || {};
  }

  async function loadDashboard() {
    setLoading(true);
    setError("");

    try {
      var sessionId = state.selectedSessionId;
      var overviewURL = ENDPOINTS.overview;
      var connectionsURL = withSession(ENDPOINTS.connections, sessionId);
      var messagesURL = withSession(ENDPOINTS.messages, sessionId);
      var results = await Promise.all([
        requestJSON(overviewURL),
        requestJSON(connectionsURL),
        requestJSON(messagesURL),
      ]);

      state.overview = results[0] || {};
      state.connectionsSnapshot = results[1] || {};
      state.messagesSnapshot = results[2] || {};
      updateKnownSessionIds();
      renderAll();
    } catch (error) {
      setError(readableError(error));
    } finally {
      setLoading(false);
    }
  }

  function withSession(url, sessionId) {
    if (!sessionId) {
      return url;
    }
    var separator = url.indexOf("?") === -1 ? "?" : "&";
    return url + separator + "sessionId=" + encodeURIComponent(sessionId);
  }

  function renderEmptyShell() {
    renderOverview({});
    renderConnections([]);
    renderMessages([]);
  }

  function renderAll() {
    var connections = getConnections();
    var messages = getMessages();
    renderOverview(state.overview || {});
    renderSessionSelect();
    renderConnections(connections);
    renderMessages(messages);
  }

  function renderOverview(overview) {
    var ws = objectValue(overview.ws);
    var connections = getConnections();
    var messages = getMessages();
    var latestConnection = objectValue(ws.latestConnection);
    var generatedAt = firstValue(
      overview.generatedAt,
      state.connectionsSnapshot && state.connectionsSnapshot.generatedAt,
      state.messagesSnapshot && state.messagesSnapshot.generatedAt
    );

    setText(dom.generatedAt, generatedAt ? "生成时间 " + formatTime(generatedAt) : "未加载");

    var totalSessions = state.knownSessionIds.length || collectSessionIds(connections, messages).length;
    var recentMessageCount = messages.length || arrayValue(ws.recentMessages).length;
    var onlineConnections = firstValue(ws.connectionCount, state.connectionsSnapshot && state.connectionsSnapshot.connectionCount, 0);
    var status = generatedAt ? "正常" : "未加载";

    var metrics = [
      { label: "服务状态", value: status, tone: generatedAt ? "ok" : "warn" },
      { label: "在线连接数", value: onlineConnections },
      { label: "会话数", value: totalSessions },
      { label: "最近消息数", value: recentMessageCount },
      { label: "启动时间", value: formatOptionalTime(firstValue(overview.startedAt, overview.startTime, overview.bootTime)) },
      { label: "运行时长", value: formatDurationValue(firstValue(overview.uptimeMs, overview.uptime, overview.durationMs)) },
      { label: "最新连接", value: latestConnection.sessionId || "未提供" },
      { label: "连接快照", value: connections.length + " 条" },
      { label: "消息快照", value: messages.length + " 条" },
      { label: "overview.generatedAt", value: generatedAt || "未提供" },
      { label: "ws.recentMessages", value: arrayValue(ws.recentMessages).length },
      { label: "筛选 session", value: state.selectedSessionId || "全部" },
    ];

    dom.overviewGrid.replaceChildren();
    metrics.forEach(function (metric) {
      var item = create("div", "metric");
      var label = create("div", "metric-label");
      var value = create("div", "metric-value");
      if (metric.tone === "ok") {
        value.classList.add("ok");
      } else if (metric.tone === "warn") {
        value.classList.add("warn");
      }
      setText(label, metric.label);
      setText(value, normalizeDisplayValue(metric.value));
      item.append(label, value);
      dom.overviewGrid.appendChild(item);
    });
  }

  function renderSessionSelect() {
    var previous = state.selectedSessionId;
    var options = [optionElement("", "全部 session")];
    state.knownSessionIds.forEach(function (sessionId) {
      options.push(optionElement(sessionId, sessionId));
    });
    dom.sessionSelect.replaceChildren.apply(dom.sessionSelect, options);
    if (previous && state.knownSessionIds.indexOf(previous) === -1) {
      dom.sessionSelect.appendChild(optionElement(previous, previous + "（当前筛选）"));
    }
    dom.sessionSelect.value = previous;
    dom.clearFilter.disabled = !previous || state.loading;
  }

  function renderConnections(connections) {
    dom.connectionsBody.replaceChildren();
    setText(dom.connectionsCount, connections.length + " 条");
    dom.connectionsEmpty.hidden = connections.length !== 0;

    connections.forEach(function (connection) {
      var row = document.createElement("tr");
      appendCell(row, connection.sessionId, "mono");
      appendCell(row, firstValue(connection.connectionId, connection.connId, connection.id, connection.sessionId), "mono");
      appendStackCell(row, [
        ["kind", connection.kind],
        ["subject", connection.subject],
        ["gateway", connection.gatewayId],
        ["channel", connection.channel],
        ["source", connection.source],
        ["device", connection.deviceId],
        ["userAgent", connection.userAgent],
      ]);
      appendCell(row, formatOptionalTime(connection.connectedAt), "mono");
      appendCell(row, formatOptionalTime(firstValue(connection.lastSeenAt, connection.lastMessageAt, connection.closedAt)), "mono");
      appendStatusCell(row, connection);
      appendCell(row, firstValue(connection.remoteAddress, connection.remoteAddr, connection.addr), "mono");
      appendStackCell(row, [
        ["in", connection.receivedMessages],
        ["out", connection.sentMessages],
        ["errors", connection.errors],
      ]);
      appendStackCell(row, [
        ["inflight", connection.inflightRequests],
        ["streams", connection.activeStreams],
        ["queue", connection.writeQueueDepth],
      ]);
      dom.connectionsBody.appendChild(row);
    });
  }

  function renderMessages(messages) {
    dom.messagesBody.replaceChildren();
    setText(dom.messagesCount, messages.length + " 条");
    dom.messagesEmpty.hidden = messages.length !== 0;

    messages.forEach(function (message) {
      var row = document.createElement("tr");
      appendCell(row, formatOptionalTime(firstValue(message.time, message.createdAt, message.timestamp)), "mono");
      appendCell(row, message.sessionId, "mono");
      appendDirectionCell(row, message.direction);
      appendCell(row, firstValue(message.type, message.frame), "mono");
      appendStackCell(row, [
        ["frame", message.frame],
        ["event", message.event],
        ["topic", message.topic],
        ["id", message.id],
        ["size", formatBytes(message.sizeBytes)],
        ["error", message.error],
      ]);
      appendPayloadCell(row, message);
      dom.messagesBody.appendChild(row);
    });
  }

  function appendCell(row, value, className) {
    var cell = document.createElement("td");
    if (className) {
      cell.className = className;
    }
    setText(cell, normalizeDisplayValue(value));
    row.appendChild(cell);
  }

  function appendStackCell(row, items) {
    var cell = document.createElement("td");
    var stack = create("div", "stack");
    items.forEach(function (item) {
      var label = item[0];
      var value = normalizeDisplayValue(item[1]);
      if (value === "-") {
        return;
      }
      var line = create("div", "mono");
      setText(line, label + ": " + value);
      stack.appendChild(line);
    });
    if (!stack.childElementCount) {
      setText(stack, "-");
    }
    cell.appendChild(stack);
    row.appendChild(cell);
  }

  function appendStatusCell(row, connection) {
    var cell = document.createElement("td");
    var chip = create("span", "chip");
    var active = Boolean(firstValue(connection.active, connection.online, connection.connected));
    chip.classList.add(active ? "chip-active" : "chip-closed");
    setText(chip, active ? "active" : "closed");
    cell.appendChild(chip);
    if (connection.closedAt) {
      var closedAt = create("div", "mono");
      setText(closedAt, "closedAt: " + formatTime(connection.closedAt));
      cell.appendChild(closedAt);
    }
    row.appendChild(cell);
  }

  function appendDirectionCell(row, direction) {
    var cell = document.createElement("td");
    var chip = create("span", "chip");
    var normalized = String(direction || "-");
    if (normalized === "in") {
      chip.classList.add("chip-in");
    } else if (normalized === "out") {
      chip.classList.add("chip-out");
    }
    setText(chip, normalized);
    cell.appendChild(chip);
    row.appendChild(cell);
  }

  function appendPayloadCell(row, message) {
    var cell = document.createElement("td");
    var fullPayload = firstValue(message.payload, message.data, message.body, "");
    var raw = firstValue(fullPayload, message.payloadPreview, "");
    var text = stringifyPayload(raw);
    var summaryText = summarize(text, 180);

    if (!text) {
      setText(cell, "-");
      row.appendChild(cell);
      return;
    }

    var summary = create("div", "payload-summary");
    setText(summary, summaryText);
    cell.appendChild(summary);

    if (message.truncated || text.length > 180) {
      var details = document.createElement("details");
      var summaryToggle = document.createElement("summary");
      setText(summaryToggle, fullPayload ? "查看完整内容" : "查看 payload 预览");
      var pre = create("pre", "payload-full");
      setText(pre, text);
      details.append(summaryToggle, pre);
      cell.appendChild(details);
    }
    row.appendChild(cell);
  }

  function updateKnownSessionIds() {
    var seen = new Set(state.knownSessionIds);
    collectSessionIds(getConnections(), getMessages()).forEach(function (sessionId) {
      seen.add(sessionId);
    });
    state.knownSessionIds = Array.from(seen).sort();
  }

  function collectSessionIds(connections, messages) {
    var seen = new Set();
    connections.forEach(function (item) {
      if (item && item.sessionId) {
        seen.add(String(item.sessionId));
      }
    });
    messages.forEach(function (item) {
      if (item && item.sessionId) {
        seen.add(String(item.sessionId));
      }
    });
    return Array.from(seen).sort();
  }

  function getConnections() {
    var snapshot = state.connectionsSnapshot || {};
    return arrayValue(firstValue(snapshot.connections, snapshot.items, snapshot.list));
  }

  function getMessages() {
    var snapshot = state.messagesSnapshot || {};
    return arrayValue(firstValue(snapshot.messages, snapshot.items, snapshot.list));
  }

  function setLoading(loading) {
    state.loading = loading;
    if (!dom.loadingState) {
      return;
    }
    dom.loadingState.hidden = !loading;
    dom.refreshButton.disabled = loading;
    dom.tokenInput.disabled = loading;
    dom.applyToken.disabled = loading;
    dom.clearToken.disabled = loading || !state.accessToken;
    dom.sessionSelect.disabled = loading;
    dom.clearFilter.disabled = loading || !state.selectedSessionId;
  }

  function applyTokenFromInput() {
    state.accessToken = normalizedAccessToken(dom.tokenInput.value);
    dom.tokenInput.value = state.accessToken;
    storeToken(state.accessToken);
    loadDashboard();
  }

  function normalizedAccessToken(value) {
    var token = String(value || "").trim();
    if (token.toLowerCase().indexOf("bearer ") === 0) {
      token = token.slice(7).trim();
    }
    return token;
  }

  function loadStoredToken() {
    try {
      return normalizedAccessToken(window.sessionStorage.getItem(TOKEN_STORAGE_KEY));
    } catch (error) {
      return "";
    }
  }

  function storeToken(token) {
    try {
      if (token) {
        window.sessionStorage.setItem(TOKEN_STORAGE_KEY, token);
      } else {
        window.sessionStorage.removeItem(TOKEN_STORAGE_KEY);
      }
    } catch (error) {
      // The token still works for the current page even if storage is blocked.
    }
  }

  function setError(message) {
    if (!message) {
      dom.errorBanner.hidden = true;
      setText(dom.errorBanner, "");
      return;
    }
    dom.errorBanner.hidden = false;
    setText(dom.errorBanner, message);
  }

  function optionElement(value, label) {
    var option = document.createElement("option");
    option.value = value;
    setText(option, label);
    return option;
  }

  function create(tagName, className) {
    var element = document.createElement(tagName);
    if (className) {
      element.className = className;
    }
    return element;
  }

  function setText(element, value) {
    element.textContent = normalizeDisplayValue(value);
  }

  function objectValue(value) {
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  }

  function arrayValue(value) {
    return Array.isArray(value) ? value : [];
  }

  function firstValue() {
    for (var i = 0; i < arguments.length; i += 1) {
      var value = arguments[i];
      if (value !== undefined && value !== null && value !== "") {
        return value;
      }
    }
    return "";
  }

  function normalizeDisplayValue(value) {
    if (value === undefined || value === null || value === "") {
      return "-";
    }
    if (typeof value === "boolean") {
      return value ? "true" : "false";
    }
    return String(value);
  }

  function readableError(error) {
    if (!error) {
      return "未知错误";
    }
    return error.message || String(error);
  }

  function formatOptionalTime(value) {
    return value ? formatTime(value) : "未提供";
  }

  function formatTime(value) {
    var date;
    if (typeof value === "number") {
      date = new Date(value > 100000000000 ? value : value * 1000);
    } else {
      date = new Date(value);
    }
    if (Number.isNaN(date.getTime())) {
      return String(value);
    }
    return date.toLocaleString();
  }

  function formatDurationValue(value) {
    if (value === undefined || value === null || value === "") {
      return "未提供";
    }
    if (typeof value === "number") {
      return formatDuration(value);
    }
    return String(value);
  }

  function formatDuration(milliseconds) {
    var totalSeconds = Math.floor(milliseconds / 1000);
    var days = Math.floor(totalSeconds / 86400);
    var hours = Math.floor((totalSeconds % 86400) / 3600);
    var minutes = Math.floor((totalSeconds % 3600) / 60);
    var seconds = totalSeconds % 60;
    var parts = [];
    if (days) {
      parts.push(days + "d");
    }
    if (hours || parts.length) {
      parts.push(hours + "h");
    }
    if (minutes || parts.length) {
      parts.push(minutes + "m");
    }
    parts.push(seconds + "s");
    return parts.join(" ");
  }

  function formatBytes(value) {
    if (value === undefined || value === null || value === "") {
      return "";
    }
    var bytes = Number(value);
    if (!Number.isFinite(bytes)) {
      return value;
    }
    if (bytes < 1024) {
      return bytes + " B";
    }
    if (bytes < 1024 * 1024) {
      return (bytes / 1024).toFixed(1) + " KB";
    }
    return (bytes / 1024 / 1024).toFixed(1) + " MB";
  }

  function stringifyPayload(value) {
    if (value === undefined || value === null) {
      return "";
    }
    if (typeof value === "string") {
      return prettyJSONString(value);
    }
    try {
      return JSON.stringify(value, null, 2);
    } catch (error) {
      return String(value);
    }
  }

  function prettyJSONString(value) {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch (error) {
      return value;
    }
  }

  function summarize(text, maxLength) {
    if (text.length <= maxLength) {
      return text;
    }
    return text.slice(0, maxLength - 1) + "...";
  }
})();
