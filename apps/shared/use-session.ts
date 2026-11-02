import { useEffect, useState } from 'preact/hooks';
import type { PublicState, ServerMessage } from './types';

export function useSession() {
  const [state, setState] = useState<PublicState>();
  const [online, setOnline] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    let socket: WebSocket | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempts = 0;
    let stopped = false;
    let paused = false;
    const connect = () => {
      if (stopped || paused) return;
      const current = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`);
      socket = current;
      current.onopen = () => { if (socket !== current || stopped || paused) return; attempts = 0; setOnline(true); setError(''); };
      current.onmessage = event => {
        if (socket !== current || stopped || paused) return;
        try {
          const message = JSON.parse(event.data) as ServerMessage;
          if (message.type === 'state') {
            if (!message.state?.seats?.A || !message.state.seats.B || !Array.isArray(message.state.recentEvents)) throw new Error('Invalid state');
            setState(message.state); setError('');
          }
        } catch { setError('Could not read the server update. Reload this page.'); }
      };
      current.onclose = () => {
        if (socket !== current || stopped || paused) return;
        setOnline(false);
        setError('Server connection lost. Displayed data may be out of date. Reconnecting automatically…');
        timer = setTimeout(connect, Math.min(1000 * 2 ** attempts++, 10_000));
      };
      current.onerror = () => current.close();
    };
    const hide = () => { paused = true; clearTimeout(timer); socket?.close(); setOnline(false); };
    const show = () => { if (paused) { paused = false; connect(); } };
    window.addEventListener('pagehide', hide);
    window.addEventListener('pageshow', show);
    connect();
    return () => { stopped = true; clearTimeout(timer); socket?.close(); window.removeEventListener('pagehide', hide); window.removeEventListener('pageshow', show); };
  }, []);
  return { state, online, error };
}
