let socket, context, action, settings = {};
function connectElgatoStreamDeckSocket(port, uuid, registerEvent, info, actionInfo) {
  context = uuid;
  try { const i = JSON.parse(actionInfo); action = i.action; settings = i.payload.settings || {}; } catch (_) {}
  socket = new WebSocket('ws://127.0.0.1:' + port);
  socket.onopen = () => {
    socket.send(JSON.stringify({ event: registerEvent, uuid }));
    document.dispatchEvent(new CustomEvent('lw-ready', { detail: settings }));
    socket.send(JSON.stringify({ event: 'sendToPlugin', context, action, payload: { request: 'scan' } }));
  };
  socket.onmessage = e => { try { const m=JSON.parse(e.data); if (m.event === 'sendToPropertyInspector') document.dispatchEvent(new CustomEvent('lw-catalog',{detail:m.payload})); } catch (_) {} };
}
function save(change) { settings = Object.assign({}, settings, change); if (socket && socket.readyState === 1) socket.send(JSON.stringify({event:'setSettings',context,payload:settings})); }
