// Builds the Web IDE into public/assets/ide (`make ide`); it is served from disk, never from the binary.
import {defineConfig} from 'vite';
import {fileURLToPath} from 'node:url';

export default defineConfig({
  root: import.meta.dirname,
  base: './',
  publicDir: false,
  logLevel: 'warn',
  build: {
    target: 'es2022',
    outDir: fileURLToPath(new URL('../../public/assets/ide', import.meta.url)),
    emptyOutDir: true,
    assetsInlineLimit: 0,
    chunkSizeWarningLimit: 10000,
    reportCompressedSize: false,
    rollupOptions: {
      input: fileURLToPath(new URL('index.ts', import.meta.url)),
      output: {
        entryFileNames: 'ide.js',
        chunkFileNames: 'chunks/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
  worker: {format: 'es'},
  plugins: [{
    // the workbench adds its styles itself (adoptedStyleSheets), so it imports them as text
    name: 'vscode-css-as-string',
    enforce: 'pre',
    async resolveId(source, importer, options) {
      const resolved = await this.resolve(source, importer, options);
      if (resolved && /node_modules\/@codingame\/monaco-vscode.*\.css$/.test(resolved.id)) return {...resolved, id: `${resolved.id}?inline`};
      return undefined;
    },
  }],
  resolve: {
    // jschardet (LGPL) only guesses file encodings, which the IDE doesn't do; it is not shipped
    alias: {jschardet: fileURLToPath(new URL('jschardet.ts', import.meta.url))},
  },
});
