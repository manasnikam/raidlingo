import { render } from 'preact';
import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks';
import { useSession } from '../../shared/use-session';
import { isLanguage, languageNames, type Activity, type Language } from '../../shared/types';
import './style.css';

function ChatLine({ event, language }: { event: Activity; language: Language }) {
  const translated = event.translations?.[language];
  return <li class={`chat-line seat-${event.seat}`}><div class="chat-meta"><span class="flag">{event.flag ?? '🌐'}</span><strong>{event.username}</strong><span class="source">{event.seat} · @{event.channel}</span></div><p class="message" lang={translated ? language : event.detectedLanguage} title={translated ?? event.text}>{translated ?? event.text}</p>{translated && event.detectedLanguage !== language ? <p class="original" lang={event.detectedLanguage} title={event.text}>{event.text}</p> : event.translationStatus !== 'ready' && <p class="original">{event.translationStatus === 'pending' ? 'Translating…' : 'Translation unavailable · original message'}</p>}</li>;
}
export function Overlay() {
  const { state, online, error } = useSession();
  const requested = new URLSearchParams(location.search).get('lang');
  const language = isLanguage(requested) ? requested : state?.overlayLanguage ?? 'en';
  const [now, setNow] = useState(Date.now());
  const [viewport, setViewport] = useState(`${innerWidth}:${innerHeight}`);
  const [visibleCount, setVisibleCount] = useState(5);
  const feed = useRef<HTMLOListElement>(null);
  const previousLayout = useRef('');
  const events = state?.recentEvents.filter(event => event.type === 'channel.chat.message').slice(-5) ?? [];
  const layout = JSON.stringify([language, viewport, events.map(event => [event.id, event.translations, event.translationStatus])]);
  useEffect(() => {
    document.body.classList.toggle('preview', new URLSearchParams(location.search).has('preview'));
    const timer = setInterval(() => setNow(Date.now()), 1000);
    const resize = () => setViewport(`${innerWidth}:${innerHeight}:${Date.now()}`);
    window.addEventListener('resize', resize);
    let active = true;
    void document.fonts?.ready.then(() => { if (active) resize(); });
    return () => { active = false; clearInterval(timer); window.removeEventListener('resize', resize); };
  }, []);
  useLayoutEffect(() => {
    if (previousLayout.current !== layout) {
      previousLayout.current = layout;
      if (visibleCount !== 5) { setVisibleCount(5); return; }
    }
    if (feed.current && visibleCount > 1 && feed.current.scrollHeight > feed.current.clientHeight + 1) setVisibleCount(count => count - 1);
  }, [layout, visibleCount]);
  const receiving = state ? Object.values(state.seats).filter(seat => seat.eventsub.subscriptions['channel.chat.message'] === 'enabled').length : 0;
  const connection = error ? online ? 'Unable to read chat update.' : 'Chat disconnected · reconnecting…' : !online ? 'Connecting to chat…' : receiving === 2 ? '' : `Waiting for ${receiving === 1 ? 'the other channel' : 'channel connections'}…`;
  const toasts = state?.recentEvents.filter(event => event.type !== 'channel.chat.message' && (event.expiresAt ?? 0) > now).slice(-2) ?? [];
  return <main class="broadcast"><div class="preview-copy" aria-hidden="true"><span>YOUR STREAM HERE</span><p>The browser source background is transparent.<br/>Remove ?preview=1 for OBS.</p></div>{state?.twitchMode === 'simulated' && <div id="simulation">LOCAL SIMULATION · FIXED TRANSLATION FIXTURES</div>}
    <section id="toasts" aria-label="Raids and subscribers">{toasts.map(event => {
      const primary = event.translations?.[language] ?? event.text;
      const otherLanguage = state!.seats[event.seat === 'A' ? 'B' : 'A'].language;
      const secondaryLanguage = language === otherLanguage ? state!.seats[event.seat].language : otherLanguage;
      const secondary = event.translations?.[secondaryLanguage];
      return <article key={event.id} class="toast"><span class="toast-label">{event.type === 'channel.raid' ? 'RAID INCOMING' : 'NEW SUBSCRIBER'} · @{event.channel}</span><strong lang={language}>{primary}</strong>{secondary && secondary !== primary && <p lang={secondaryLanguage}>{secondary}</p>}</article>;
    })}</section>
    <section class="chat-panel" aria-label="Translated merged chat"><header><strong>raidlingo<span>.</span></strong><div><span class={`live-dot ${online && receiving === 2 ? 'connected' : ''}`}/><span id="channel-label">CHAT TOGETHER</span></div></header>{connection && <div id="connection" role="status">{connection}</div>}<ol id="feed" ref={feed}>{events.length ? events.slice(-visibleCount).map(event => <ChatLine key={`${event.seat}:${event.id}`} event={event} language={language}/>) : <li class="empty">Two communities, one conversation. Waiting for the first message…</li>}</ol><footer><span id="languages">{state ? `${state.seats.A.language.toUpperCase()} ↔ ${state.seats.B.language.toUpperCase()}` : 'EN ↔ ES'}</span><span id="view-language">{languageNames[language]} feed</span></footer></section>
  </main>;
}
render(<Overlay/>, document.querySelector('#app')!);
