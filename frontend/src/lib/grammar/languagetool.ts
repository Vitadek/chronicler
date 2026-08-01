// Client side of the grammar checker. The engine is now self-hosted
// LanguageTool living behind the Chronicler server (/api/grammar/check), so the
// client just POSTs text and gets hits back — no WASM, no worker, nothing to
// download to the device.
//
// Web runs same-origin and reads the bearer from localStorage automatically.
// The mobile editor bundle (served locally in a WebView, no app localStorage)
// calls setGrammarEndpoint(serverBaseUrl, token) once before enabling.

export interface GrammarHit {
  /** Char offset into the linted text (inclusive). */
  start: number;
  /** Char offset into the linted text (exclusive). */
  end: number;
  /**
   * misspelling | grammar | typographical | style | confusion | …
   *
   * Mostly LanguageTool's issue type, except `confusion` — word-confusion pairs
   * (quiet/quite), which LT calls misspellings but which are correctly spelled
   * words and must not be dictionary-suppressed. See server/routes/grammar.ts.
   */
  kind: string;
  message: string;
  /** Correction candidates (misspellings and confusions), capped server-side. */
  replacements?: string[];
  /** Populated only by the optional LanguageTool engine; absent for native hits. */
  ruleId?: string;
  category?: string;
  sourceId?: string;
  sourceLabel?: string;
  groupId?: string;
}

export interface GrammarProvider {
  id: string;
  label: string;
  adapter: string;
  dataBoundary: 'local' | 'cloud';
  modes: ('standard' | 'picky')[];
  defaultEnabled: boolean;
  allowBackground: boolean;
  available: boolean;
  error?: string;
}

export interface GrammarProviderRun {
  id: string;
  status: 'ok' | 'unavailable' | 'invalid' | 'timeout' | 'rate_limited';
  durationMs: number;
  fromCache?: boolean;
  error?: string;
}

export interface GrammarProviderSelection {
  id: string;
  mode?: EnhancedGrammarLevel;
}

export interface GrammarProviderResult {
  hits: GrammarHit[];
  providers: GrammarProviderRun[];
}

let endpointBase = '';
let authToken: string | null = null;

/**
 * Configure where grammar requests go and how they authenticate. Web doesn't
 * need to call this (same-origin + localStorage token); the mobile bundle does.
 */
export function setGrammarEndpoint(base: string, token?: string | null): void {
  endpointBase = (base || '').replace(/\/+$/, '');
  if (token !== undefined) authToken = token;
}

function bearer(): string | null {
  if (authToken) return authToken;
  try {
    return typeof localStorage !== 'undefined' ? localStorage.getItem('chronicle_token') : null;
  } catch {
    return null;
  }
}

/** No engine to load (server-side); kept for API parity with the old loader. */
export async function loadGrammarEngine(): Promise<void> {
  /* no-op */
}

/**
 * Lint a chunk of text via the server's grammar endpoint, distinguishing a
 * failed request (non-OK response, or a thrown fetch error — offline,
 * timeout, sidecar cold/restarting) from a genuinely clean paragraph: `null`
 * means "unknown, try again later" and must never be cached as "no issues".
 */
export type EnhancedGrammarLevel = 'standard' | 'picky';

export async function lintTextOrNull(text: string, opts?: { engine?: 'native' | 'languagetool'; level?: EnhancedGrammarLevel }): Promise<GrammarHit[] | null> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = bearer();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  try {
    const res = await fetch(`${endpointBase}/api/grammar/check`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ text, ...(opts?.engine ? { engine: opts.engine } : {}), ...(opts?.level ? { level: opts.level } : {}) }),
    });
    if (!res.ok) return null;
    const data = (await res.json()) as { hits?: GrammarHit[] };
    return data.hits || [];
  } catch {
    return null;
  }
}

/**
 * Lint a chunk of text via the server's grammar endpoint. Part of the plugin
 * API surface (src/plugins/host/PluginHost.tsx, src/plugins/api/index.ts) so
 * its non-nullable `Promise<GrammarHit[]>` signature must stay as-is; callers
 * that need to distinguish failure from "no issues" (Grammar.ts's cache)
 * should use `lintTextOrNull` instead.
 */
export async function lintText(text: string, opts?: { engine?: 'native' | 'languagetool'; level?: EnhancedGrammarLevel }): Promise<GrammarHit[]> {
  return (await lintTextOrNull(text, opts)) ?? [];
}

export async function lintWithProviders(text: string, providers: GrammarProviderSelection[]): Promise<GrammarProviderResult> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = bearer();
  if (token) headers.Authorization = `Bearer ${token}`;
  try {
    const res = await fetch(`${endpointBase}/api/grammar/check`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ text, providers }),
    });
    if (!res.ok) return { hits: [], providers: providers.map(({ id }) => ({ id, status: 'unavailable', durationMs: 0 })) };
    const data = (await res.json()) as Partial<GrammarProviderResult>;
    return { hits: data.hits ?? [], providers: data.providers ?? [] };
  } catch {
    return { hits: [], providers: providers.map(({ id }) => ({ id, status: 'unavailable', durationMs: 0 })) };
  }
}

interface GrammarCapabilities {
  languagetool: { available: boolean };
}

/** Whether the optional LanguageTool sidecar is currently reachable. */
export async function fetchGrammarCapabilities(): Promise<GrammarCapabilities> {
  const headers: Record<string, string> = {};
  const token = bearer();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  try {
    const res = await fetch(`${endpointBase}/api/grammar/capabilities`, { headers });
    if (!res.ok) return { languagetool: { available: false } };
    return (await res.json()) as GrammarCapabilities;
  } catch {
    return { languagetool: { available: false } };
  }
}

export async function fetchGrammarProviders(): Promise<GrammarProvider[]> {
  const headers: Record<string, string> = {};
  const token = bearer();
  if (token) headers.Authorization = `Bearer ${token}`;
  try {
    const res = await fetch(`${endpointBase}/api/grammar/providers`, { headers });
    if (!res.ok) return [];
    const data = (await res.json()) as { providers?: GrammarProvider[] };
    return data.providers ?? [];
  } catch {
    return [];
  }
}
