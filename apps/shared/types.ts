export type SeatId = 'A' | 'B';
export type Language = 'en' | 'es' | 'hi';
export type SubscriptionType = 'channel.chat.message' | 'channel.raid' | 'channel.subscribe';
export const languageNames: Record<Language, string> = { en: 'English', es: 'Español', hi: 'हिन्दी' };
export function isLanguage(value: string | null): value is Language { return value === 'en' || value === 'es' || value === 'hi'; }
export interface EventSubStatus { connection: string; sessionId?: string; subscriptions: Partial<Record<SubscriptionType, string>>; error?: string }
export interface SeatStatus { seat: SeatId; language: Language; expectedChannel?: string; login?: string; authorized: boolean; scopes: string[]; eventsub: EventSubStatus; messagesReceived: number; error?: string }
export interface BotStatus { authorized: boolean; expectedChannel?: string; login?: string; error?: string }
export interface Activity {
  id: string; seat: SeatId; type: SubscriptionType; channel: string; username: string; text: string; receivedAt: string; targetLanguage: Language;
  detectedLanguage?: string; flag?: string; translated?: string; translations?: Partial<Record<Language, string>>;
  translationStatus: 'pending' | 'ready' | 'error'; translationError?: string; cached?: boolean; expiresAt?: number;
  delivery: { status: 'queued' | 'sending' | 'rate_limited' | 'sent' | 'error' | 'dropped' | 'skipped'; error?: string; messageId?: string };
}
export interface PublicState {
  phase: 2; twitchMode: 'live' | 'simulated'; voice: { configuredMode: 'realtime' | 'pipeline' | 'off'; available: false };
  seats: Record<SeatId, SeatStatus>; bot: BotStatus;
  relay: { ready: boolean; error?: string; queued: number; sent: number; failed: number; dropped: number; rateLimited: number; ignored: number };
  translation: { provider: 'openai' | 'codex'; model: string; hits: number; misses: number; requests: number; errors: number; entries: number; inFlight: number; pending: number };
  recentEvents: Activity[]; overlayUrl: string; overlayLanguage: Language;
}
export type ServerMessage = { type: 'state'; state: PublicState } | { type: 'twitch.event'; event: Activity };
