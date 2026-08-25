import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  // Relative URLs so the Wails webview can load JS/CSS from the embedded dist.
  // `base: '/'` produces `/assets/...` which 404s in production and paints a blank window.
  base: './',
  build: {
    // WKWebView on the macOS versions this app ships for speaks modern JS.
    // Targeting es2022 keeps Vite from injecting downlevel helpers.
    target: 'es2022',
    // Served from the embedded filesystem into a modern WebKit webview:
    // the modulepreload polyfill is dead weight in the bundle.
    modulePreload: {polyfill: false},
    // Local webview / phone LAN: gzip size in the build log is not useful,
    // and computing it slows every `wails build`.
    reportCompressedSize: false,
  },
})
