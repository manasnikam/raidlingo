import { defineConfig, loadEnv } from 'vite';
export default defineConfig(({ mode, command }) => {
  const env = loadEnv(mode, '../..', '');
  const target = `http://127.0.0.1:${env.PORT || 3000}`;
  return { base: command === 'build' ? '/overlay/' : '/', server: { proxy: {
    '/api': { target, changeOrigin: true }, '/ws': { target, ws: true, changeOrigin: true },
  } } };
});
