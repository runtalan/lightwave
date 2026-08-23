// Shared Property Inspector plumbing: connect, then persist form fields as
// action settings. Stream Deck calls connectElgatoStreamDeckSocket on load.
let ws, ctx, current = {};

function connectElgatoStreamDeckSocket(port, uuid, registerEvent, info, actionInfo) {
  ctx = uuid;
  try { current = JSON.parse(actionInfo).payload.settings || {}; } catch (e) { current = {}; }
  ws = new WebSocket('ws://127.0.0.1:' + port);
  ws.onopen = () => {
    ws.send(JSON.stringify({ event: registerEvent, uuid }));
    document.dispatchEvent(new CustomEvent('pi-ready', { detail: current }));
  };
}

function save(patch) {
  current = Object.assign({}, current, patch);
  if (!ws || ws.readyState !== 1) return;
  ws.send(JSON.stringify({ event: 'setSettings', context: ctx, payload: current }));
}

function bindField(el, key, coerce) {
  el.addEventListener('change', () => save({ [key]: coerce ? coerce(el.value) : el.value }));
}
