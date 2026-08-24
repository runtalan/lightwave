// Lightwave web bridge.
//
// The desktop bundle reaches Go through window.go.main.App and window.runtime.
// This file supplies both over HTTP and runs before the bundle (it is a classic
// script, the bundle is a deferred module), so the identical React build boots
// unmodified in a phone browser.
(function () {
  'use strict'

  // A token in the URL is saved once and stripped, so the page can be
  // bookmarked or added to the home screen without carrying it around.
  var token = null
  try {
    var url = new URL(window.location.href)
    var fromURL = url.searchParams.get('token')
    if (fromURL) {
      localStorage.setItem('lw.token', fromURL)
      url.searchParams.delete('token')
      history.replaceState(null, '', url.pathname + url.search)
    }
    token = localStorage.getItem('lw.token')
  } catch (e) {
    /* private mode: fall through with no token */
  }

  function call(method, args) {
    var headers = { 'Content-Type': 'application/json' }
    if (token) headers['X-Lightwave-Token'] = token
    return fetch('_lw/call', {
      method: 'POST',
      headers: headers,
      body: JSON.stringify({ method: method, args: args }),
    })
      .then(function (r) {
        if (!r.ok) throw new Error('lightwave: ' + r.status)
        return r.json()
      })
      .then(function (j) {
        if (j && j.error) throw new Error(j.error)
        return j ? j.result : undefined
      })
  }

  // Control calls, forwarded to the app. The server keeps its own allowlist —
  // this list only decides what makes a round trip.
  var REMOTE = [
    'GetState', 'GetSlots', 'GetActivePool', 'GetPaletteIndex', 'Dancing',
    'ToggleSlot', 'ToggleAll', 'AllOn', 'AllOff',
    'SetBrightness', 'CycleColor', 'ToggleDance', 'ToggleGradient',
  ]

  // Everything else resolves locally without touching the network. A phone has
  // no window to drag, hide, or quit, never opens Config, and must not be able
  // to rewrite the pad map or the stored API key. Resolving rather than
  // throwing keeps the shared UI's optimistic `void Call()` sites quiet.
  var LOCAL = [
    'MarkUIReady', 'PingActivity', 'PingMotion', 'StartWindowDrag', 'EndWindowDrag',
    'HideHUD', 'ShowHUD', 'ToggleWindow', 'Quit', 'OpenConfig', 'OpenSetup',
    'CloseConfig', 'CancelSetup', 'IsSetupOpen', 'HandleIPC', 'RemoteCommand',
    'SetIPCServer', 'Discover', 'ScanLAN', 'GetDevices', 'AssignSlot', 'MoveSlot',
    'RenameSlot', 'FillRemaining', 'SaveMappings', 'CommitMappings', 'SaveSettings',
    'SetConfigAPIKey',
  ]

  var App = {}
  REMOTE.forEach(function (m) {
    App[m] = function () {
      return call(m, Array.prototype.slice.call(arguments))
    }
  })
  LOCAL.forEach(function (m) {
    App[m] = function () {
      return Promise.resolve()
    }
  })
  window.go = { main: { App: App } }

  // Event plumbing. The desktop gets these from the Wails runtime; here they
  // arrive on an SSE stream, which reconnects on its own when a phone wakes or
  // changes network.
  var listeners = {}
  function emit(name, data) {
    var cbs = listeners[name]
    if (!cbs) return
    for (var i = 0; i < cbs.length; i++) {
      try {
        cbs[i](data)
      } catch (e) {
        /* one bad listener must not stop the rest */
      }
    }
  }

  var noop = function () {}
  window.runtime = {
    EventsOn: function (name, cb) {
      ;(listeners[name] = listeners[name] || []).push(cb)
      return function () {
        delete listeners[name]
      }
    },
    EventsOff: function (name) {
      delete listeners[name]
    },
    EventsOnce: function (name, cb) {
      var wrapped = function (d) {
        delete listeners[name]
        cb(d)
      }
      ;(listeners[name] = listeners[name] || []).push(wrapped)
    },
    EventsOnMultiple: function (name, cb) {
      ;(listeners[name] = listeners[name] || []).push(cb)
      return noop
    },
    EventsOffAll: function () {
      listeners = {}
    },
    EventsEmit: noop,
    LogPrint: noop, LogTrace: noop, LogDebug: noop, LogInfo: noop,
    LogWarning: noop, LogError: noop, LogFatal: noop,
    WindowSetAlwaysOnTop: noop, WindowShow: noop, WindowHide: noop,
    WindowCenter: noop, WindowReload: noop, WindowReloadApp: noop,
    Quit: noop, Hide: noop, Show: noop,
  }

  try {
    var src = '_lw/events'
    if (token) src += '?token=' + encodeURIComponent(token)
    var es = new EventSource(src)
    es.onmessage = function (ev) {
      var msg
      try {
        msg = JSON.parse(ev.data)
      } catch (e) {
        return
      }
      if (msg && msg.event) emit(msg.event, msg.data)
    }
  } catch (e) {
    // No stream: the UI still works, it just will not update itself until the
    // next control call returns fresh state.
  }

  // Hide the affordances that only mean something on the desktop: the Config
  // button (this build is control-only) and the hide/quit legend entries,
  // which have no counterpart on a phone.
  var style = document.createElement('style')
  style.textContent =
    '.config-launch{display:none!important}' +
    '[data-desktop-only]{display:none!important}' +
    // No window chrome to drag, and the tall HUD should use the whole screen.
    '.titlebar{display:none!important}' +
    '.hud-shell{padding:0}' +
    '.panel{border-radius:0;border:0}'
  document.head.appendChild(style)
})()
