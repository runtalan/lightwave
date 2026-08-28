// Shared Property Inspector plumbing: connect, then persist form fields as
// action settings. Stream Deck calls connectElgatoStreamDeckSocket on load.
let ws, ctx, action, current = {};

function connectElgatoStreamDeckSocket(port, uuid, registerEvent, info, actionInfo) {
  ctx = uuid;
  try { current = JSON.parse(actionInfo).payload.settings || {}; } catch (e) { current = {}; }
  try { action = JSON.parse(actionInfo).action; } catch (e) { action = ''; }
  ws = new WebSocket('ws://127.0.0.1:' + port);
  ws.onopen = () => {
    ws.send(JSON.stringify({ event: registerEvent, uuid }));
    document.dispatchEvent(new CustomEvent('pi-ready', { detail: current }));
  };
  // The plugin pushes data the inspector cannot fetch itself — it has no route
  // to Lightwave's socket — so re-broadcast those payloads as a DOM event.
  ws.onmessage = (msg) => {
    let m;
    try { m = JSON.parse(msg.data); } catch (e) { return; }
    if (m.event === 'sendToPlugin' || m.event === 'sendToPropertyInspector') {
      document.dispatchEvent(new CustomEvent('pi-data', { detail: m.payload || {} }));
    }
  };
}

// askPlugin requests a fresh payload from the plugin, for a Property Inspector
// that opens before the plugin has state to send.
function askPlugin(payload) {
  if (!ws || ws.readyState !== 1) return;
  ws.send(JSON.stringify({ event: 'sendToPlugin', context: ctx, action, payload: payload || {} }));
}

function save(patch) {
  current = Object.assign({}, current, patch);
  if (!ws || ws.readyState !== 1) return;
  ws.send(JSON.stringify({ event: 'setSettings', context: ctx, payload: current }));
}

function bindField(el, key, coerce) {
  el.addEventListener('change', () => save({ [key]: coerce ? coerce(el.value) : el.value }));
}
