# Raidlingo

Two Twitch streamers. Two languages. One conversation.

**Go backend + Preact 11 frontends.** The Go HTTP server owns OAuth, tokens, EventSub, translation, rate-limited posting, and browser WebSockets. The desk and OBS overlay use Preact, TypeScript, and Vite. The backend has no external Go module dependencies.

**Phase 2 is implemented:** both channels feed a translated chat view, a separate bot posts translations into the opposite channel, and OBS shows a merged feed plus bilingual raid/sub toasts. Phase 1 OAuth/EventSub features remain in place. **Microphones, voice dubbing, voice subtitles, and the fallback audio pipeline are not implemented yet.**

## Run the local demo without credentials

Requires **Go 1.22+**, **Node 22.16+**, and npm. Node is used for frontend builds and development tooling; the production server is a Go binary.

```sh
cd /Users/manasnikam/project/hackathon/twitch/raidlingo
npm ci
npm run demo:phase2
```

Open [the connection desk](http://127.0.0.1:3000). It displays a permanent **LOCAL SIMULATION** banner. Connect the three mock identities using the buttons:

1. Seat A → `streamer_a`.
2. Seat B → `streamer_b`.
3. Relay bot → `raidlingo_bot`.

Sample English/Spanish messages arrive every six seconds. The desk shows originals, translations, delivery status, cache hits, and sent counts. Repeated samples hit the cache. Raid/sub toasts appear periodically. Choose **Overlay language**, then open the translated overlay. English, Spanish, and Hindi views use the same cached bundle.

The Go simulator exercises the real Go server and Preact apps against local HTTP/WebSocket fixtures. It does not call Twitch or OpenAI, and the fixed translations do not verify model quality. Test tokens are stored in a temporary directory removed on shutdown. `npm run demo:phase1` remains an alias for the current fixture. Stop the demo before starting a real server on port 3000.

## Run with real Twitch and Codex CLI

```sh
# Only for a new checkout; keep an existing .env.
cp .env.example .env
# Fill in the Twitch credentials and channel logins below.
codex login
npm run check:live
npm run dev
```

Required `.env` keys:

```dotenv
TWITCH_CLIENT_ID=your_twitch_app_client_id
TWITCH_CLIENT_SECRET=your_twitch_app_client_secret
TRANSLATION_PROVIDER=codex
CHANNEL_A=first_broadcaster_login
CHANNEL_B=second_broadcaster_login
BOT_CHANNEL=your_separate_bot_login
LANGUAGE_A=en
LANGUAGE_B=es
OVERLAY_LANGUAGE=en
STREAM_HINTS=Raidlingo,Twitch,YourGameName,ImportantUsername
```

The local Codex provider uses the CLI's saved authentication. Sign in with your ChatGPT account using `codex login`; check an existing session with `codex login status`. An OpenAI API key is not required for this option. The adapter was checked against Codex CLI **0.164.0** and requires `--ignore-user-config` support. See [Codex authentication](https://learn.chatgpt.com/docs/auth) and [non-interactive execution](https://learn.chatgpt.com/docs/non-interactive-mode).

`npm run check:live` builds the Go server, checks the Twitch app credentials, and translates one sample sentence. It does not post to Twitch or authorize the broadcaster/bot accounts. The sample consumes the selected provider's allowance. A failed network check does not establish whether credentials are valid.

The server reads the root `.env`; process environment variables take precedence. Pass `--root /path/to/raidlingo` when running a copied binary from another directory. Secrets stay server-side; do not use `VITE_` keys for them. Missing required secrets cause an explicit startup error. `BOT_CHANNEL` is an optional expected login, while the bot OAuth connection itself is required to post messages. Blank channel values or placeholders bind to whichever account authorizes the seat; configured logins reject the wrong identity.

### Translation provider options

Codex runs one fresh, ephemeral CLI process per uncached message, with a **20-second timeout** and no parallel CLI requests. It uses a temporary working directory, disabled tools, a filtered environment, and a strict output schema. Twitch credentials are not passed to the child process. The CLI reads its own saved authentication; Raidlingo does not copy those credentials. Local configuration is ignored for this child without changing your saved settings or managed requirements. CLI warning and planning items are treated as metadata; only a completed turn with valid translation JSON succeeds. Command execution, file changes, external tool calls, and unknown item types cause the response to be discarded.

Use `CODEX_BIN` to select the executable, `CODEX_TRANSLATE_MODEL` to override the CLI's built-in default model, and `CODEX_TIMEOUT_MS` to set a timeout between 1000 and 30000 milliseconds. Subscription usage limits still apply. Startup and model latency can make this option unsuitable for busy chat; it does not provide voice translation.

The existing API provider remains available:

```dotenv
TRANSLATION_PROVIDER=openai
OPENAI_API_KEY=your_openai_api_key
OPENAI_TRANSLATE_MODEL=gpt-4o-mini
```

Existing configurations without `TRANSLATION_PROVIDER` continue to use `openai`. The supplied `.env.example` selects `codex` explicitly.

### Twitch setup

Use a **Confidential** Twitch application in **Broadcaster Suite**, with this exact registered callback:

```text
http://localhost:3000/callback
```

Set the same URL in `TWITCH_REDIRECT_URI`. `npm run dev` starts Go plus both Vite servers, reloads Preact changes, and rebuilds/restarts Go when `.go` files change. Open [the development desk](http://localhost:5173), connect both broadcasters, then connect a **third, separate Twitch account** as the bot. Separate browser profiles or private windows help switch accounts. Twitch `force_verify` requests consent; it does not automatically change the signed-in account.

Broadcaster scopes:

```text
user:read:chat user:write:chat user:bot channel:bot
moderator:read:chatters channel:read:subscriptions
```

Bot scopes:

```text
user:write:chat user:bot
```

Each seat retains its broadcaster-owned EventSub WebSocket. The extra `channel:read:subscriptions` scope is required for subscription events. Non-eligible channels may show a subscription error while chat continues working.

Posting uses an app access token with the bot's prior `user:write:chat`/`user:bot` grant and each broadcaster's `channel:bot` grant. `sender_id` is always the separate bot; `for_source_only=true` keeps translated posts in the destination channel during Twitch Shared Chat. No moderator privileges are assumed. See [Twitch Send Chat Message](https://dev.twitch.tv/docs/api/reference/#send-chat-message) and [EventSub authorization](https://dev.twitch.tv/docs/eventsub/eventsub-subscription-types/).

**When all three accounts are connected, new chat messages are automatically translated and posted.** Messages received before the bot/destination was connected are shown in the feed but are never replayed into chat later.

### What the relay does

1. Receive each channel's original chat message via EventSub; discard the bot's own messages and Shared Chat mirrors.
2. Check the normalized LRU cache. On a miss, the selected provider detects the actual language and returns English, Spanish, and Hindi text in one structured JSON response.
3. Send the opposite seat's language to its channel as `🇪🇸 username: translated text`. The flag represents the detected source language; unknown languages use 🌐.
4. Push the original, all language versions, cache status, and posting result to the browser clients. Translation failures retain the original and do not post anything.

The prompt asks the model to preserve game names, usernames, URLs, emoji, and emotes. `STREAM_HINTS` supplies a comma-separated glossary; both broadcaster names are also included. Chat text is treated as untrusted text to translate, not as commands to obey. Both providers share the same translation instructions, output schema, validation, and cache.

The cache holds up to **1000** entries for **30 minutes**, normalizes whitespace, and includes the source hint and glossary in its key. Case and Unicode characters remain distinct so URLs, emotes, and names cannot reuse a different message's translation. Concurrent repeats share one pending request. All three output languages share the entry. Originals retain their exact case/spacing; errors are never cached. The API provider uses [OpenAI structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs?api-mode=chat) with `store:false`; Codex uses `--output-schema` and validates its completed JSON response before accepting it.

### Posting limits and failures

The outbox spaces attempts **1600ms apart globally**, conservatively staying within a regular bot's 20 messages/30 seconds and one message/second/channel limits. It queues at most 100 messages, expires queued work after 45 seconds, and checks account assignments again before sending. Translation backlog is also bounded, with a 30-second budget including queue time and provider execution. Use a dedicated bot so another application does not spend the same account's limit.

HTTP 429 respects Twitch's reset/retry headers and gets at most two retries. The final 429 still delays subsequent queued messages after that message exhausts its retries. A timeout is not automatically retried because Twitch may already have accepted the post. An HTTP 200 with `is_sent:false` is a posting error, not success. All failures appear on the desk. See [Twitch chat rate limits](https://dev.twitch.tv/docs/chat/#rate-limits).

## URLs and OBS

| Development URL | Purpose |
| --- | --- |
| [Desk](http://localhost:5173) | Authorizations, merged chat, delivery/errors, cache counters |
| [Seat A](http://localhost:5173/?seat=A) / [Seat B](http://localhost:5173/?seat=B) | Highlight a streamer seat; no mic controls yet |
| [English overlay](http://localhost:5174/?lang=en) | Transparent, merged English feed |
| [Spanish overlay](http://localhost:5174/?lang=es) | Transparent, merged Spanish feed |
| [Hindi overlay](http://localhost:5174/?lang=hi) | Transparent, merged Hindi feed |
| [Overlay preview](http://localhost:5174/?preview=1&lang=en) | Checkerboard backdrop to inspect the overlay |
| [Health](http://localhost:3000/health) | Process liveness, Twitch chat subscription readiness, relay authorization readiness |
| [Public state](http://localhost:3000/api/state) | Last 100 events and status, never tokens or API keys |

Create an OBS **Browser Source**, set **1920×1080**, and use the overlay URL for your preferred language. Remove `preview=1` for a transparent background. The overlay shows up to five complete chat messages, translated text, smaller originals when languages differ, and source attribution. Very long lines are clipped for broadcast readability; the desk retains full text. The latest two raid/sub notifications expire after 12 seconds and use built-in localized text without a translation request.

To serve the built apps from a single port:

```sh
npm run build
npm start
```

The build creates `bin/raidlingo` and both `apps/*/dist` directories. Keep those asset directories with the binary; `bin/raidlingo --production --root /path/to/raidlingo` serves them directly. The desk becomes `http://localhost:3000/` and overlay `http://localhost:3000/overlay/?lang=en`. The simulator uses these same built routes. Loopback binding is the default.

### Open from other devices on the same network

Stop the running Raidlingo server with **Ctrl+C** in its terminal, then use your computer's private IPv4 address:

```sh
npm run start:lan -- 192.168.1.14
```

This builds the apps and starts one Go server on port 3000. From a device on the same Wi-Fi or LAN, open:

- Desk: `http://192.168.1.14:3000/`
- OBS overlay: `http://192.168.1.14:3000/overlay/?lang=en`
- Overlay preview: `http://192.168.1.14:3000/overlay/?preview=1&lang=en`

Replace `192.168.1.14` if your computer's address changes. On this Mac, `ipconfig getifaddr en0` prints the Wi-Fi IPv4 address. Keep the terminal running. If another device cannot connect, check that both devices are on the same network, that guest/client isolation is off for that network, and that the host firewall allows Raidlingo's incoming connection.

LAN mode binds to `0.0.0.0` and sets the desk/overlay URLs plus their allowed host and WebSocket origin. It keeps the configured `PORT` and does not edit `.env`. Existing builds can be launched directly with `bin/raidlingo --production --lan-ip 192.168.1.14`. Only private IPv4 addresses are supported; LAN mode does not run the simulator.

**Twitch account authorization still happens on the host Mac with the current setup.** The registered callback is `http://localhost:3000/callback`; on a phone, `localhost` means the phone. Connect all three accounts from the Mac, then view their shared desk and overlay on the other devices. Signing in from another device requires a callback URL reachable from that device, registered with Twitch and matched in `TWITCH_REDIRECT_URI`. See [Twitch's authorization-code flow](https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/#authorization-code-grant-flow).

## Verification

```sh
npm run typecheck
npm test
npm run build
```

`npm test` runs the Go unit and integration suite with the race detector. Tests exercise real HTTP/WebSocket framing over `net.Pipe`, so they do not require credentials, outbound network access, or permission to bind local ports. Coverage includes OAuth/state/account checks, token rotation and restoration, validation recovery, EventSub retries/handoffs, both relay directions, Hindi, cache concurrency/LRU/expiry and case-sensitive content, bot/Shared Chat dedupe, final-429 cooldowns, translation refusals, stale queues, browser state streaming, and localized toasts. Codex subprocess tests cover stdin/environment isolation, malformed or incomplete output, tool-action rejection, output limits, cancellation, and cache reuse using a fake CLI executable. `npm run typecheck` checks both Preact apps and runs `go vet`.

Live acceptance, once credentials are configured:

1. Connect both broadcasters and the bot; confirm chat subscriptions show Receiving.
2. Send an English message in A's real Twitch chat; confirm Spanish output appears in B under the bot account and the desk reports Sent.
3. Send Spanish in B; confirm English appears in A. Repeat with Hindi by setting `LANGUAGE_B=hi` and restarting.
4. Ask the same question repeatedly; cache hits should increase without extra provider calls. Bot echoes should not enter the feed or bounce back.
5. Open the OBS overlay and check translation readability and source attribution. Observe an actual raid/sub event when available.

**Live Twitch app credentials, Codex translation, and relay posting in both directions passed** on 2026-11-03. The initial terminal translation check completed in 5.3 seconds and returned English, Spanish, and Hindi. The live desk was then verified with `roxor028` and `manas0222` receiving chat and `samadhan0077` authorized. The bot visibly posted "Hola, ¿cómo estás?" in `manas0222`'s chat for an English message from channel A, then "I'm ready to play." in `roxor028`'s chat for a Spanish message from channel B (preserving the user's leading quotation mark). The desk showed 2 sent and 0 posting errors. English and Spanish browser overlay previews showed the corresponding translations and originals. This verifies the two sample messages, not linguistic accuracy across arbitrary chat or sustained throughput. The generated test card was verified in OBS and on the live Twitch channel. The translated Browser Source is now above the image in OBS and visibly renders English text, Spanish originals, and source attribution over the test card. The user subsequently confirmed completion of the requested phone playback check. Independent public-player visual verification was unavailable because Safari and Chrome showed a black video area during inspection; the Twitch dashboard reported Excellent stream health. Stop the local simulator before starting the live server; the live desk should show the configured account names without a LOCAL SIMULATION banner.

The narrated demo is available at [outputs/demo/raidlingo-demo.mp4](outputs/demo/raidlingo-demo.mp4), with [English subtitles](outputs/demo/raidlingo-demo.srt). It runs 2 minutes 35 seconds at 1280×720 and shows the connected desk, a newly recorded English-to-Spanish delivery, the earlier verified Spanish-to-English example (labeled as an earlier live test), and the OBS overlay. The new message, "Welcome to our bilingual stream!", appeared in channel B as "¡Bienvenidos a nuestro stream bilingüe!"; the desk reached 3 sent and 0 posting errors. The reverse example uses retained live history because a new message could not be submitted through the second channel's browser controls. OBS was restored to its original test-card and overlay view after recording, with streaming still active. Narration uses the local macOS Samantha synthetic voice. Raw takes, narration, subtitles, edit metadata, and export scripts are kept under `outputs/demo/`; the development presentation is `apps/seat/demo-recording.html`.

## Troubleshooting

| Symptom | Action |
| --- | --- |
| Missing credentials | Fill the Twitch credentials in the root `.env`; `OPENAI_API_KEY` is required only for `TRANSLATION_PROVIDER=openai`. |
| Codex missing / sign-in required | Check `CODEX_BIN`, run `codex login`, then `npm run check:live` from your terminal. |
| Codex configuration incompatible | Check CLI version and configured model; the adapter was checked against version 0.164.0. |
| Codex unsupported item | The response was discarded. Share the named event type from `npm run check:live`; raw prompts and tool arguments are not included in the error. |
| Codex usage limit / timeout | Check account allowance and connectivity; reduce chat volume. Timed-out translations are not posted. |
| Invalid OAuth state | Start Connect again in the same browser. State expires in 10 minutes and after a server restart. |
| Wrong account / duplicate seat | Use distinct accounts for A, B, and the bot; check expected logins in `.env`. |
| Missing scopes / revoked grant | Reconnect the affected account and approve its requested permissions. |
| Translation works, no posts | Connect the bot and both seats; inspect each line's delivery status. Unsent history is not replayed. |
| OpenAI HTTP 401 / 429 | Check the key, model access, billing/quota, or rate limits. New messages recover after the issue is fixed. |
| Twitch HTTP 403 | Check bot/broadcaster grants, bans, and channel chat restrictions. |
| Held by AutoMod / another drop reason | Twitch has not sent the line. Resolve the channel restriction; the app does not automatically retry dropped messages. |
| Rate limited / queue full / stale | Reduce input volume; queued messages are bounded and stale posts are discarded visibly. |
| Subscriptions fail but chat receives | Check broadcaster eligibility and `channel:read:subscriptions`. |
| Twitch validation or subscription temporarily unavailable | Network errors, timeouts, HTTP 408/429, and 5xx responses retry automatically with exponential backoff up to 30 seconds, or longer when Twitch requests it. Validation failures pause relay until the account validates again. Revoked grants and permission errors still require reconnection. |
| EventSub reconnecting | Check connectivity. Subscriptions restore after transport loss; Twitch does not replay events missed while disconnected. |
| Blank/partial overlay | Verify the correct development vs built URL, add `preview=1`, and check incoming events on the desk. |

Existing `.env` files and version-1 `.data/twitch-tokens.json` files remain compatible. Tokens stay in ignored `.data/twitch-tokens.json`, written atomically with mode `0600`. The token file retains the A/B/`bot` slots. User tokens are validated at startup/hourly and refreshed when expired or rejected; app tokens are generated/cached server-side. OAuth request URLs are not logged. Stop the server and move the token file aside if it is corrupted, then authorize all three accounts again.

## Code map and next phases

```text
cmd/raidlingo/       Go entry point, signals, live/demo startup
internal/raidlingo/  Config, token store, OAuth/Helix/EventSub, translation, outbox, server, simulator
internal/ws/         Uncompressed RFC 6455 text transport, framing/handshake tests
internal/testutil/   HTTP over in-memory connections for integration tests
apps/seat/          Preact connection desk and merged event monitor
apps/overlay/       Preact transparent OBS feed and localized toasts
apps/shared/        Browser state contract and reconnecting Preact hook
scripts/            Build/dev process management; no application backend logic
BUILD_LOG.md        Meaningful build steps and decisions
```

| Phase | Status |
| --- | --- |
| 1 | Implemented: scaffold, OAuth, EventSub, two-channel intake |
| 2 — current | Implemented: text translation/cache/glossary, bot posting, merged overlay/toasts |
| 3 | Next: PCM16 mic capture, Realtime speech-to-speech, playback, subtitles, latency |
| 4 | Pending: transcription → translation → TTS fallback and operational voice modes |
| 5 | Pending: broadcast polish, reconnect hardening, demo finishing touches |

`VOICE_MODE=realtime|pipeline|off` remains configuration-only; `voice.available` is still false. Phase 2 work stops for review before voice implementation.
