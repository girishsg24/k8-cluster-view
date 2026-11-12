async function getJSON(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(url + " -> " + res.status);
  return res.json();
}

function renderSummary(s) {
  const phases = Object.entries(s.podsByPhase || {})
    .map(([k, v]) => `${k}: ${v}`).join(" · ") || "none";
  document.getElementById("summary").innerHTML =
    `<b>Nodes:</b> ${s.nodeCount} (ready ${s.readyNodes}, not-ready ${s.notReadyNodes}) ` +
    `&nbsp; <b>Pods:</b> ${s.podCount} &nbsp; <b>Phases:</b> ${phases}` +
    (s.lastUpdated ? ` &nbsp; <small>updated ${new Date(s.lastUpdated).toLocaleTimeString()}</small>` : "");
}

function renderNodes(nodes) {
  const tb = document.querySelector("#nodes tbody");
  tb.innerHTML = "";
  for (const n of nodes) {
    const tr = document.createElement("tr");
    tr.className = n.name === selectedNode ? "node selected" : "node";
    tr.innerHTML =
      `<td>${n.name}</td>` +
      `<td class="${n.ready ? "ok" : "bad"}">${n.ready ? "Ready" : "NotReady"}</td>` +
      `<td>${(n.roles || []).join(", ")}</td>` +
      `<td>${n.cpuAllocatable} / ${n.cpuCapacity}</td>` +
      `<td>${n.memoryAllocatable} / ${n.memoryCapacity}</td>` +
      `<td>${n.podCount}</td>`;
    tr.onclick = () => selectNode(n.name, tr);
    tb.appendChild(tr);
  }
}

// Remembered across the periodic re-render so the highlight and the pod
// panel stay on the node the user clicked.
let selectedNode = null;

async function selectNode(name, tr) {
  selectedNode = name;
  document.querySelectorAll("tr.node.selected").forEach(e => e.classList.remove("selected"));
  if (tr) tr.classList.add("selected");
  document.getElementById("pods-title").textContent = `Pods on ${name}`;
  const pods = await getJSON(`/api/v1/nodes/${encodeURIComponent(name)}/pods`);
  const tb = document.querySelector("#pods tbody");
  tb.innerHTML = "";
  for (const p of pods) {
    const tr2 = document.createElement("tr");
    tr2.innerHTML =
      `<td>${p.namespace}</td><td>${p.name}</td>` +
      `<td class="phase-${p.phase}">${p.phase}</td><td>${p.restartCount}</td>`;
    tb.appendChild(tr2);
  }
  if (!pods.length) tb.innerHTML = `<tr><td colspan="4">No pods.</td></tr>`;
}

async function refresh() {
  try {
    renderSummary(await getJSON("/api/v1/summary"));
    renderNodes(await getJSON("/api/v1/nodes"));
    if (selectedNode) await selectNode(selectedNode, document.querySelector("tr.node.selected"));
  } catch (e) {
    document.getElementById("summary").textContent = "Error: " + e.message;
  }
}

refresh();
setInterval(refresh, 10000);
