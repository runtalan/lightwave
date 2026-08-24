package web

import _ "embed"

// bridgeJS is served at /_lw/bridge.js and installs the window.go /
// window.runtime shims the frontend bundle expects. Embedding keeps it in the
// binary alongside the bundle it adapts.
//
//go:embed bridge.js
var bridgeJS []byte
