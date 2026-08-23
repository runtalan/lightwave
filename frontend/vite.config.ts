import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // Relative URLs so the Wails webview can load JS/CSS from the embedded dist.
  // `base: '/'` produces `/assets/...` which 404s in production and paints a blank window.
  base: './',
})
