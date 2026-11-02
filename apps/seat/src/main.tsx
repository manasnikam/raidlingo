import { render } from 'preact';
import { useState } from 'preact/hooks';
import { useSession } from '../../shared/use-session';
import { languageNames, type Activity, type Language, type SeatStatus } from '../../shared/types';
import './style.css';

function Seat({ seat, selected }: { seat: SeatStatus; selected: string | null }) {
  const ready = seat.eventsub.subscriptions['channel.chat.message'] === 'enabled';
  const error = seat.error ?? seat.eventsub.error;
  return <article class={`seat ${selected === seat.seat ? 'selected' : ''}`} aria-label={`Seat ${seat.seat}`}>
    <div class="seat-heading"><span class="seat-letter">{seat.seat}</span><div class="identity"><p class="eyebrow">SEAT {seat.seat}{selected === seat.seat ? ' · YOUR SEAT' : ''}</p><h2>{seat.login ? `@${seat.login}` : seat.expectedChannel ? `@${seat.expectedChannel}` : 'Connect a streamer'}</h2></div><span class="language">{languageNames[seat.language]}</span></div>
    <div class="seat-state"><span class={`status ${ready ? 'ready' : ''}`}>{seat.authorized ? seat.eventsub.connection : 'Not connected'}</span><span class="muted message-count">{seat.messagesReceived} chat messages</span></div>
    <ul class="checks">{([['channel.chat.message', 'Chat messages'], ['channel.raid', 'Incoming raids'], ['channel.subscribe', 'Subscriptions']] as const).map(([type, label]) => <li key={type} class={seat.eventsub.subscriptions[type] === 'enabled' ? 'enabled' : ''}><span>{label}</span><span>{seat.eventsub.subscriptions[type] === 'enabled' ? 'Receiving' : seat.eventsub.subscriptions[type] ? 'Needs attention' : 'Awaiting connection'}</span></li>)}</ul>
    {error && <p class={seat.authorized || seat.eventsub.connection === 'error' ? 'seat-error' : 'seat-hint'}>{error}</p>}
    <a class="connect" href={`/auth/twitch?seat=${seat.seat}`} aria-label={`${seat.authorized ? 'Reconnect' : 'Connect'} Twitch for seat ${seat.seat}`}>{seat.authorized ? 'Reconnect' : 'Connect'} Twitch ↗</a>
  </article>;
}
function EventRow({ event, language }: { event: Activity; language: Language }) {
  return <li class="event"><span class="event-seat">{event.seat}</span><time dateTime={event.receivedAt}>{new Date(event.receivedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time><div class="event-content"><strong>{event.username} </strong><span class="muted">in @{event.channel} · {event.type.replace('channel.', '')}</span><p class="original">{event.text}</p>
    {event.type === 'channel.chat.message' ? <>
      {event.translationStatus === 'ready' ? <p class="translated" lang={language}>{event.flag} {event.translations?.[language] ?? event.translated}</p> : <p class={event.translationStatus === 'error' ? 'seat-error' : 'muted'}>{event.translationStatus === 'pending' ? 'Translating…' : `Translation unavailable: ${event.translationError}`}</p>}
      <span class={event.delivery.status === 'error' ? 'seat-error' : 'muted'}>{event.cached ? 'Cache hit · ' : ''}Relay: {event.delivery.status}{event.delivery.error ? ` · ${event.delivery.error}` : ''}</span>
    </> : event.translations && <p class="translated" lang={language}>{event.translations[language]}</p>}
  </div></li>;
}
export function Desk() {
  const { state, online, error } = useSession();
  const selected = new URLSearchParams(location.search).get('seat');
  const [chosenLanguage, setLanguage] = useState<Language>();
  const language = chosenLanguage ?? state?.overlayLanguage ?? 'en';
  const invalidSeat = !!selected && selected !== 'A' && selected !== 'B';
  const notice = error || (invalidSeat ? 'Unknown seat. Use ?seat=A or ?seat=B. Showing both seats below.' : '');
  const overlay = new URL(state?.overlayUrl ?? '/overlay/', location.href);
  overlay.searchParams.set('preview', '1'); overlay.searchParams.set('lang', language);
  return <>
    <header class="masthead"><a class="brand" href="/" aria-label="Raidlingo home"><span class="brand-mark" aria-hidden="true">r↔</span>raidlingo<span class="brand-period">.</span></a><span class="edition">TWITCH × TWO LANGUAGES</span></header>
    <main><section class="intro"><div><p class="eyebrow">THE CONNECTION DESK <span>02 / 05</span></p><h1>Two channels.<br/>One conversation.</h1><p class="lede">Bring your streams together. Start by connecting<br class="desktop-break"/> the Twitch account for each seat.</p></div><aside class="phase-note"><span class="phase-number">02</span><div><strong>Now, bring the chats together.</strong><p>Translated chat is ready to test.<br/>Voice dubbing comes next.</p><span id="server-state" role="status" class={online ? 'online' : ''}>{online ? '● Server connected' : error ? '○ Server disconnected' : 'Connecting to server…'}</span></div></aside></section>
    {state?.twitchMode === 'simulated' && <div id="simulation" class="notice">LOCAL SIMULATION — These are test Twitch accounts and sample events. Translations are fixed test fixtures. Real Twitch and OpenAI have not been connected.</div>}
    {notice && <div id="notice" class="notice" role="alert">{notice}</div>}
    <section aria-label="Streamer connections" class="seats" id="seats">{state && ['A', 'B'].map(id => <Seat key={id} seat={state.seats[id as 'A' | 'B']} selected={selected}/>)}</section>
    <section class="relay-desk" aria-label="Translation relay"><div id="bot">{state && <div class="bot-identity"><p class="eyebrow">THE RELAY BOT</p><h2>{state.bot.login ? `@${state.bot.login}` : state.bot.expectedChannel ? `@${state.bot.expectedChannel}` : 'Give your translations a voice in chat'}</h2><p class={state.bot.authorized ? 'seat-hint' : 'seat-error'}>{state.bot.error ?? 'Authorized to post translated chat in both channels.'}</p><a class="bot-connect" href="/auth/twitch?seat=bot">{state.bot.authorized ? 'Reconnect' : 'Connect'} bot account ↗</a></div>}</div>
      <div class="relay-settings"><label for="overlay-language">Overlay language</label><select id="overlay-language" value={language} onChange={event => setLanguage(event.currentTarget.value as Language)}>{Object.entries(languageNames).map(([code, name]) => <option key={code} value={code}>{name}</option>)}</select><p id="relay-status" role="status">{state ? state.relay.ready ? '● Relay ready · new messages will be posted' : state.relay.error : 'Waiting for server…'}</p><p id="relay-stats" class="muted">{state && `${state.relay.sent} sent · ${state.relay.queued} queued · ${state.translation.hits} cache hits · ${state.relay.failed} posting errors`}</p></div></section>
    <section class="monitor"><div class="section-heading"><div><p class="eyebrow">SIGNAL CHECK</p><h2>The merged conversation</h2></div><span id="event-count" class="muted">{state ? `${state.seats.A.messagesReceived + state.seats.B.messagesReceived} chat messages received` : 'Waiting for connection'}</span></div><p class="monitor-help">Every original stays visible. Translations relay to the other channel once both streamers and the bot are connected.</p><div id="events" aria-label="Recent Twitch events">{state?.recentEvents.length ? <ol class="event-list">{state.recentEvents.slice(-30).reverse().map(event => <EventRow key={`${event.seat}:${event.id}`} event={event} language={language}/>)}</ol> : <div class="empty"><span aria-hidden="true">↗</span><div><strong>Your first message starts here.</strong><p>Connect both streamers, then type in either Twitch chat.<br/>The message will appear here.</p></div></div>}</div></section>
    <footer><span>BUILT TO CROSS THE LANGUAGE BARRIER.</span><a id="overlay-link" href={overlay.toString()} target="_blank" rel="noreferrer">Open translated overlay ↗</a></footer></main>
  </>;
}
render(<Desk/>, document.querySelector('#app')!);
