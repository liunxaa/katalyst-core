const qosLevels = ["shared_cores", "dedicated_cores", "reclaimed_cores", "system_cores"];

let scenario = null;
let trace = null;
let activeStepIndex = 0;
let activeTab = "overview";

const els = {
  runButton: document.getElementById("runButton"),
  topologyCPUs: document.getElementById("topologyCPUs"),
  topologySockets: document.getElementById("topologySockets"),
  topologyNUMAs: document.getElementById("topologyNUMAs"),
  reservedCPUs: document.getElementById("reservedCPUs"),
  reservedReclaimedCPUsSize: document.getElementById("reservedReclaimedCPUsSize"),
  hintSelection: document.getElementById("hintSelection"),
  initReservePool: document.getElementById("initReservePool"),
  initReclaimPool: document.getElementById("initReclaimPool"),
  enableReclaim: document.getElementById("enableReclaim"),
  enableReclaimNUMABinding: document.getElementById("enableReclaimNUMABinding"),
  allowSharedCoresOverlapReclaimedCores: document.getElementById(
    "allowSharedCoresOverlapReclaimedCores",
  ),
  snbCPUThresholdRatio: document.getElementById("snbCPUThresholdRatio"),
  requestQoS: document.getElementById("requestQoS"),
  requestCPU: document.getElementById("requestCPU"),
  requestPodName: document.getElementById("requestPodName"),
  requestContainerName: document.getElementById("requestContainerName"),
  requestContainerType: document.getElementById("requestContainerType"),
  requestHintNodes: document.getElementById("requestHintNodes"),
  requestNUMABinding: document.getElementById("requestNUMABinding"),
  requestSkipAllocate: document.getElementById("requestSkipAllocate"),
  addRequestButton: document.getElementById("addRequestButton"),
  resetScenarioButton: document.getElementById("resetScenarioButton"),
  formatScenarioButton: document.getElementById("formatScenarioButton"),
  scenarioEditor: document.getElementById("scenarioEditor"),
  summaryStrip: document.getElementById("summaryStrip"),
  stepCount: document.getElementById("stepCount"),
  flowList: document.getElementById("flowList"),
  emptyState: document.getElementById("emptyState"),
  stepDetail: document.getElementById("stepDetail"),
  detailTitle: document.getElementById("detailTitle"),
  detailMeta: document.getElementById("detailMeta"),
  detailStatus: document.getElementById("detailStatus"),
  tabContent: document.getElementById("tabContent"),
};

async function boot() {
  const res = await fetch("/api/sample");
  scenario = await res.json();
  syncControlsFromScenario();
  writeScenarioEditor();
  bindEvents();
  await runTrace();
}

function bindEvents() {
  els.runButton.addEventListener("click", runTrace);
  els.addRequestButton.addEventListener("click", addRequest);
  els.resetScenarioButton.addEventListener("click", resetScenario);
  els.formatScenarioButton.addEventListener("click", formatScenarioEditor);
  els.scenarioEditor.addEventListener("change", readScenarioEditor);

  [
    els.topologyCPUs,
    els.topologySockets,
    els.topologyNUMAs,
    els.reservedCPUs,
    els.reservedReclaimedCPUsSize,
    els.hintSelection,
    els.initReservePool,
    els.initReclaimPool,
    els.enableReclaim,
    els.enableReclaimNUMABinding,
    els.allowSharedCoresOverlapReclaimedCores,
    els.snbCPUThresholdRatio,
  ].forEach((el) => {
    el.addEventListener("change", () => {
      syncScenarioFromControls();
      writeScenarioEditor();
    });
  });

  document.querySelectorAll(".tab").forEach((tab) => {
    tab.addEventListener("click", () => {
      activeTab = tab.dataset.tab;
      document.querySelectorAll(".tab").forEach((item) => item.classList.remove("active"));
      tab.classList.add("active");
      renderActiveStep();
    });
  });
}

function syncControlsFromScenario() {
  els.topologyCPUs.value = scenario.topology?.cpus ?? 16;
  els.topologySockets.value = scenario.topology?.sockets ?? 2;
  els.topologyNUMAs.value = scenario.topology?.numas ?? 4;
  els.reservedCPUs.value = scenario.reservedCPUs ?? "";
  els.reservedReclaimedCPUsSize.value = scenario.reservedReclaimedCPUsSize ?? "";
  els.hintSelection.value = scenario.hintSelection || "first-preferred";
  els.initReservePool.checked = scenario.initReservePool !== false;
  els.initReclaimPool.checked = scenario.initReclaimPool !== false;
  els.enableReclaim.checked = scenario.enableReclaim === true;
  els.enableReclaimNUMABinding.checked = scenario.enableReclaimNUMABinding !== false;
  els.allowSharedCoresOverlapReclaimedCores.checked =
    scenario.allowSharedCoresOverlapReclaimedCores === true;
  els.snbCPUThresholdRatio.value = scenario.snbCPUThresholdRatio || "";
}

function syncScenarioFromControls() {
  scenario.topology = {
    cpus: numberValue(els.topologyCPUs, 16),
    sockets: numberValue(els.topologySockets, 2),
    numas: numberValue(els.topologyNUMAs, 4),
  };
  const reserved = els.reservedCPUs.value.trim();
  if (reserved === "") {
    delete scenario.reservedCPUs;
  } else {
    scenario.reservedCPUs = Number(reserved);
  }
  const reservedReclaimed = els.reservedReclaimedCPUsSize.value.trim();
  if (reservedReclaimed === "") {
    delete scenario.reservedReclaimedCPUsSize;
  } else {
    scenario.reservedReclaimedCPUsSize = Number(reservedReclaimed);
  }
  scenario.hintSelection = els.hintSelection.value;
  scenario.initReservePool = els.initReservePool.checked;
  scenario.initReclaimPool = els.initReclaimPool.checked;
  scenario.enableReclaim = els.enableReclaim.checked;
  scenario.enableReclaimNUMABinding = els.enableReclaimNUMABinding.checked;
  scenario.allowSharedCoresOverlapReclaimedCores =
    els.allowSharedCoresOverlapReclaimedCores.checked;
  const threshold = els.snbCPUThresholdRatio.value.trim();
  if (threshold === "") {
    delete scenario.snbCPUThresholdRatio;
  } else {
    scenario.snbCPUThresholdRatio = Number(threshold);
  }
}

function readScenarioEditor() {
  try {
    scenario = JSON.parse(els.scenarioEditor.value);
    syncControlsFromScenario();
    clearEditorError();
    return true;
  } catch (err) {
    showEditorError(err.message);
    return false;
  }
}

function writeScenarioEditor() {
  els.scenarioEditor.value = JSON.stringify(scenario, null, 2);
}

function formatScenarioEditor() {
  if (readScenarioEditor()) {
    writeScenarioEditor();
  }
}

function addRequest() {
  if (!readScenarioEditor()) {
    return;
  }
  syncScenarioFromControls();
  const qos = els.requestQoS.value;
  const podName = cleanName(els.requestPodName.value, `pod-${scenario.pods.length + 1}`);
  const containerName = cleanName(els.requestContainerName.value, "main");
  const numaBinding = els.requestNUMABinding.checked;
  const hintNodes = parseHintNodes(els.requestHintNodes.value);

  const pod = {
    uid: podName,
    namespace: "default",
    name: podName,
    qos,
    numaBinding,
    containers: [
      {
        name: containerName,
        cpu: Number(els.requestCPU.value || 0),
        qos,
        containerType: els.requestContainerType.value,
        skipAllocate: els.requestSkipAllocate.checked,
      },
    ],
  };
  if (hintNodes.length > 0) {
    pod.containers[0].hintNodes = hintNodes;
  }

  scenario.pods.push(pod);
  els.requestPodName.value = `pod-${scenario.pods.length + 1}`;
  writeScenarioEditor();
}

async function resetScenario() {
  const res = await fetch("/api/sample");
  scenario = await res.json();
  syncControlsFromScenario();
  writeScenarioEditor();
  await runTrace();
}

async function runTrace() {
  if (!readScenarioEditor()) {
    return;
  }
  syncScenarioFromControls();
  writeScenarioEditor();

  els.runButton.disabled = true;
  els.runButton.textContent = "Running";
  try {
    const res = await fetch("/api/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(scenario),
    });
    const data = await res.json();
    if (!res.ok) {
      throw new Error(data.error || "run failed");
    }
    trace = data;
    activeStepIndex = 0;
    renderTrace();
  } catch (err) {
    renderRunError(err.message);
  } finally {
    els.runButton.disabled = false;
    els.runButton.textContent = "Run";
  }
}

function renderTrace() {
  renderSummary();
  renderFlow();
  renderActiveStep();
}

function renderSummary() {
  const finalEntries = trace?.finalSnapshot?.pod_entries || {};
  const poolNames = Object.keys(finalEntries).filter((key) => finalEntries[key]?.[""] || finalEntries[key]?.fake);
  const allocatedSteps = (trace?.steps || []).filter((step) => step.allocate && !step.allocateError).length;
  els.summaryStrip.innerHTML = [
    metric("Steps", String(trace?.steps?.length || 0)),
    metric("Allocated", String(allocatedSteps)),
    metric("Pools", String(poolNames.length)),
    metric("Reclaim", trace?.scenario?.enableReclaim ? "enabled" : "disabled"),
    metric(
      "Overlap",
      trace?.scenario?.allowSharedCoresOverlapReclaimedCores ? "enabled" : "disabled",
    ),
    metric("State dir", trace?.stateDirectory || ""),
  ].join("");
}

function renderFlow() {
  const steps = trace?.steps || [];
  els.stepCount.textContent = `${steps.length} steps`;
  els.flowList.innerHTML = steps
    .map((step) => {
      const statusClass = step.getTopologyHintsError || step.allocateError ? "error" : step.allocate ? "" : "warn";
      const status = step.getTopologyHintsError || step.allocateError ? "error" : step.allocate ? "allocated" : "hints";
      return `
        <button class="flow-card ${step.index === activeStepIndex ? "active" : ""}" data-step="${step.index}" type="button">
          <div class="flow-card-title">
            <span>${escapeHTML(step.podName)} / ${escapeHTML(step.containerName)}</span>
            <span class="status-pill ${statusClass}">${status}</span>
          </div>
          <div class="flow-card-meta">${escapeHTML(step.qos)} · CPU ${formatNumber(step.cpuRequest)} · NUMA ${step.numaBinding}</div>
        </button>
      `;
    })
    .join("");
  els.flowList.querySelectorAll(".flow-card").forEach((card) => {
    card.addEventListener("click", () => {
      activeStepIndex = Number(card.dataset.step);
      renderTrace();
    });
  });
}

function renderActiveStep() {
  const step = (trace?.steps || [])[activeStepIndex];
  if (!step) {
    els.emptyState.classList.remove("hidden");
    els.stepDetail.classList.add("hidden");
    return;
  }

  els.emptyState.classList.add("hidden");
  els.stepDetail.classList.remove("hidden");
  els.detailTitle.textContent = `${step.podNamespace}/${step.podName} ${step.containerName}`;
  els.detailMeta.textContent = `${step.qos} · CPU ${formatNumber(step.cpuRequest)} · NUMA binding ${step.numaBinding}`;
  const hasError = Boolean(step.getTopologyHintsError || step.allocateError);
  const status = hasError ? "error" : step.allocate ? "allocated" : "hints only";
  els.detailStatus.className = `status-pill ${hasError ? "error" : step.allocate ? "" : "warn"}`;
  els.detailStatus.textContent = status;

  switch (activeTab) {
    case "hints":
      renderHintsTab(step);
      break;
    case "allocation":
      renderAllocationTab(step);
      break;
    case "cache":
      renderCacheTab(step);
      break;
    case "diff":
      renderDiffTab(step);
      break;
    case "raw":
      renderRawTab(step);
      break;
    default:
      renderOverviewTab(step);
  }
}

function renderOverviewTab(step) {
  const hintCount = cpuHints(step).length;
  const selected = step.selectedHint ? `NUMA ${step.selectedHint.nodes?.join(",") || "none"}` : "none";
  const allocationInfo = step.afterAllocate?.allocation_info;
  const allocationResult = allocationInfo?.allocation_result || "none";
  const pool = allocationInfo?.owner_pool_name || "none";
  els.tabContent.innerHTML = `
    ${errorBanner(step)}
    <div class="kv-grid">
      ${kv("hint handler", step.hintHandler)}
      ${kv("allocation handler", step.allocationHandler)}
      ${kv("hint count", hintCount)}
      ${kv("selected hint", selected)}
      ${kv("allocation result", allocationResult)}
      ${kv("owner pool", pool)}
    </div>
    <div class="json-stack">
      ${jsonBlock("AllocationInfo", allocationInfo)}
      ${jsonBlock("Selected hint", step.selectedHint)}
    </div>
  `;
}

function renderHintsTab(step) {
  const hints = cpuHints(step);
  const cards = hints.length
    ? hints
        .map((hint) => {
          const nodes = hint.nodes?.length ? hint.nodes.join(",") : "none";
          return `
            <div class="hint-card ${hint.preferred ? "preferred" : ""}">
              <strong>${escapeHTML(nodes)}</strong>
              <span>${hint.preferred ? "preferred" : "candidate"}</span>
            </div>
          `;
        })
        .join("")
    : `<div class="empty-state"><p>No CPU NUMA preference</p></div>`;

  els.tabContent.innerHTML = `
    ${errorBanner(step)}
    <div class="hint-grid">${cards}</div>
    <div class="json-stack">${jsonBlock("GetTopologyHints response", step.getTopologyHints)}</div>
  `;
}

function renderAllocationTab(step) {
  els.tabContent.innerHTML = `
    ${errorBanner(step)}
    <div class="json-stack">
      ${jsonBlock("Allocate response", step.allocate)}
      ${jsonBlock("AllocationInfo after Allocate", step.afterAllocate?.allocation_info)}
    </div>
  `;
}

function renderCacheTab(step) {
  els.tabContent.innerHTML = `
    ${errorBanner(step)}
    <div class="cache-grid">
      <div class="cache-column">${jsonBlock("Before", compactSnapshot(step.before))}</div>
      <div class="cache-column">${jsonBlock("After hints", compactSnapshot(step.afterHints))}</div>
      <div class="cache-column">${jsonBlock("After allocate", compactSnapshot(step.afterAllocate))}</div>
    </div>
  `;
}

function renderDiffTab(step) {
  const hintDiff = diffObjects(compactSnapshot(step.before), compactSnapshot(step.afterHints), "");
  const allocateDiff = diffObjects(compactSnapshot(step.afterHints), compactSnapshot(step.afterAllocate), "");
  els.tabContent.innerHTML = `
    ${errorBanner(step)}
    <div class="diff-list">
      <div>
        <h3>Before -> After GetTopologyHints (${hintDiff.length})</h3>
        ${renderDiffItems(hintDiff)}
      </div>
      <div>
        <h3>After GetTopologyHints -> After Allocate (${allocateDiff.length})</h3>
        ${renderDiffItems(allocateDiff)}
      </div>
    </div>
  `;
}

function renderRawTab(step) {
  els.tabContent.innerHTML = `<div class="json-stack">${jsonBlock("Trace step", step)}</div>`;
}

function renderRunError(message) {
  trace = null;
  els.summaryStrip.innerHTML = metric("Run error", message);
  els.flowList.innerHTML = "";
  els.stepCount.textContent = "";
  els.emptyState.classList.remove("hidden");
  els.stepDetail.classList.add("hidden");
}

function errorBanner(step) {
  const message = step.getTopologyHintsError || step.allocateError;
  if (!message) {
    return "";
  }
  return `<div class="error-banner">${escapeHTML(message)}</div>`;
}

function cpuHints(step) {
  return step.getTopologyHints?.resource_hints?.cpu?.hints || [];
}

function compactSnapshot(snapshot) {
  if (!snapshot) {
    return null;
  }
  return {
    reserved_cpus: snapshot.reserved_cpus,
    reserved_reclaimed_cpus_size: snapshot.reserved_reclaimed_cpus_size,
    enable_reclaim: snapshot.enable_reclaim,
    allow_shared_cores_overlap_reclaimed_cores:
      snapshot.allow_shared_cores_overlap_reclaimed_cores,
    reclaim_overlap_share_ratio: snapshot.reclaim_overlap_share_ratio,
    reclaim_overlap_share_ratio_error: snapshot.reclaim_overlap_share_ratio_error,
    allocation_info: snapshot.allocation_info,
    machine_state: snapshot.machine_state,
    numa_headroom: snapshot.numa_headroom,
    pod_entries: snapshot.pod_entries,
  };
}

function diffObjects(before, after, path) {
  if (stableString(before) === stableString(after)) {
    return [];
  }
  if (!isObject(before) || !isObject(after)) {
    return [{ path: path || "$", before, after }];
  }

  const keys = Array.from(new Set([...Object.keys(before || {}), ...Object.keys(after || {})])).sort();
  const result = [];
  keys.forEach((key) => {
    const childPath = path ? `${path}.${key}` : key;
    if (!(key in (before || {}))) {
      result.push({ path: childPath, before: undefined, after: after[key] });
      return;
    }
    if (!(key in (after || {}))) {
      result.push({ path: childPath, before: before[key], after: undefined });
      return;
    }
    result.push(...diffObjects(before[key], after[key], childPath));
  });
  return result.slice(0, 80);
}

function renderDiffItems(items) {
  if (!items.length) {
    return `<div class="diff-item"><strong>No change</strong></div>`;
  }
  return items
    .map(
      (item) => `
        <div class="diff-item">
          <strong>${escapeHTML(item.path)}</strong>
          <code>before: ${escapeHTML(shortValue(item.before))}</code>
          <code>after: ${escapeHTML(shortValue(item.after))}</code>
        </div>
      `,
    )
    .join("");
}

function jsonBlock(title, value) {
  return `
    <div class="json-block">
      <h3>${escapeHTML(title)}</h3>
      <pre>${escapeHTML(JSON.stringify(value ?? null, null, 2))}</pre>
    </div>
  `;
}

function kv(label, value) {
  return `
    <div class="kv">
      <span>${escapeHTML(label)}</span>
      <strong>${escapeHTML(String(value ?? ""))}</strong>
    </div>
  `;
}

function metric(label, value) {
  return `
    <div class="metric">
      <span>${escapeHTML(label)}</span>
      <strong>${escapeHTML(String(value ?? ""))}</strong>
    </div>
  `;
}

function parseHintNodes(value) {
  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean)
    .map((item) => Number(item))
    .filter((item) => Number.isInteger(item) && item >= 0);
}

function cleanName(value, fallback) {
  const trimmed = (value || "").trim();
  return trimmed || fallback;
}

function numberValue(el, fallback) {
  const value = Number(el.value);
  return Number.isFinite(value) ? value : fallback;
}

function isObject(value) {
  return value !== null && typeof value === "object";
}

function stableString(value) {
  return JSON.stringify(value);
}

function shortValue(value) {
  if (value === undefined) {
    return "<missing>";
  }
  const text = JSON.stringify(value);
  if (text.length > 180) {
    return `${text.slice(0, 177)}...`;
  }
  return text;
}

function formatNumber(value) {
  return Number(value || 0).toFixed(3);
}

function showEditorError(message) {
  els.scenarioEditor.style.borderColor = "#a33b32";
  els.summaryStrip.innerHTML = metric("JSON error", message);
}

function clearEditorError() {
  els.scenarioEditor.style.borderColor = "";
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

boot().catch((err) => {
  renderRunError(err.message);
});
