import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import react from '@vitejs/plugin-react'

// The Go binary embeds dist/. Vite empties dist/ on every build, so emit the
// placeholder back into it: it lets `go build` succeed before the UI is built.
const keepDist: Plugin = {
  name: 'tracelet-keep-dist',
  generateBundle() {
    this.emitFile({ type: 'asset', fileName: '.gitkeep', source: '' })
  },
}

export default defineConfig({
  plugins: [react(), keepDist],
  server: {
    // `make studio-dev` runs the UI with hot reload against a running
    // `tracelet up`, which serves the API on :4000.
    proxy: { '/api': 'http://127.0.0.1:4000' },
  },
})
