import { defineConfig, loadEnv } from 'vite';
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '../..', '');
  const target = `http://127.0.0.1:${env.PORT || 3000}`;
  return { server: { proxy: {
    '/api': { target, changeOrigin: true }, '/auth': { target, changeOrigin: true },
    '/callback': { target, changeOrigin: true }, '/ws': { target, ws: true, changeOrigin: true },
  } } };
});
