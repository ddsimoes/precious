import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import type { Plugin } from 'vite'
import { defineConfig } from 'vitest/config'

// The Go server for `npm run dev`. Its CSRF check wants Origin equal to
// server.external_origin, so proxied requests carry the target's origin.
const devApi = process.env.PRECIOUS_DEV_API ?? 'http://127.0.0.1:8080'

// web/dist/.gitkeep is committed so `go:embed all:dist` compiles without a
// built UI; emptyOutDir removes it, so the build writes it back.
function keepDistPlaceholder(): Plugin {
  return {
    name: 'precious-keep-dist-placeholder',
    apply: 'build',
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: '.gitkeep', source: '' })
    },
  }
}

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDistPlaceholder()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: '../dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    // data: URLs would break img-src/font-src 'self'; every asset is a file.
    assetsInlineLimit: 0,
    // One bundle, embedded in the binary and served from the LAN: ECharts and
    // highlight.js make it larger than the default warning size.
    chunkSizeWarningLimit: 1500,
  },
  server: {
    proxy: {
      '/api': {
        target: devApi,
        configure(proxy) {
          proxy.on('proxyReq', (req) => {
            if (req.getHeader('origin') !== undefined) {
              req.setHeader('origin', devApi)
            }
          })
        },
      },
    },
  },
  test: {
    environment: 'jsdom',
    // Dates in tests read the same on every machine.
    env: { TZ: 'UTC' },
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}', 'scripts/**/*.test.mjs'],
  },
})
