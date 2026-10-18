// Shared Property Inspector plumbing: connect, then persist form fields as
// action settings. Stream Deck calls connectElgatoStreamDeckSocket on load.
let ws, ctx, action, controller, current = {}, globalSettings = {};

function connectElgatoStreamDeckSocket(port, uuid, registerEvent, info, actionInfo) {
  ctx = uuid;
  try { current = JSON.parse(actionInfo).payload.settings || {}; } catch (e) { current = {}; }
  try { action = JSON.parse(actionInfo).action; } catch (e) { action = ''; }
  // "Keypad" or "Encoder": a dial ignores key-only settings, so say so.
  try { controller = JSON.parse(actionInfo).payload.controller || 'Keypad'; } catch (e) { controller = 'Keypad'; }
  ws = new WebSocket('ws://127.0.0.1:' + port);
  ws.onopen = () => {
    ws.send(JSON.stringify({ event: registerEvent, uuid }));
    document.dispatchEvent(new CustomEvent('pi-ready', { detail: current }));
    ws.send(JSON.stringify({ event: 'getGlobalSettings', context: uuid }));
  };
  // The plugin pushes data the inspector cannot fetch itself — it has no route
  // to Lightwave's socket — so re-broadcast those payloads as a DOM event.
  ws.onmessage = (msg) => {
    let m;
    try { m = JSON.parse(msg.data); } catch (e) { return; }
    if (m.event === 'sendToPlugin' || m.event === 'sendToPropertyInspector') {
      document.dispatchEvent(new CustomEvent('pi-data', { detail: m.payload || {} }));
    }
    // Plugin-wide settings, as opposed to this key's: sent on request and again
    // whenever any inspector changes them.
    if (m.event === 'didReceiveGlobalSettings') {
      globalSettings = (m.payload && m.payload.settings) || {};
      document.dispatchEvent(new CustomEvent('pi-global', { detail: globalSettings }));
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

// saveGlobal persists plugin-wide settings. They live in Stream Deck's own
// store rather than in the profile, so a shared or exported profile does not
// carry what is saved here.
function saveGlobal(patch) {
  globalSettings = Object.assign({}, globalSettings, patch);
  if (!ws || ws.readyState !== 1) return;
  ws.send(JSON.stringify({ event: 'setGlobalSettings', context: ctx, payload: globalSettings }));
  // Tell the plugin too. Stream Deck gives it the global settings at startup but
  // does not reliably announce a later change, and a login typed after that
  // would otherwise not be used until Stream Deck restarted.
  askPlugin({ want: 'globals', globals: globalSettings });
}
