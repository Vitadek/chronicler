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
 * Lint a chunk of text via the server's LanguageTool proxy, distinguishing a
 * failed request (non-OK response, or a thrown fetch error — offline,
 * timeout, sidecar cold/restarting) from a genuinely clean paragraph: `null`
 * means "unknown, try again later" and must never be cached as "no issues".
 */
export async function lintTextOrNull(text: string): Promise<GrammarHit[] | null> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = bearer();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  try {
    const res = await fetch(`${endpointBase}/api/grammar/check`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ text }),
    });
    if (!res.ok) return null;
    const data = (await res.json()) as { hits?: GrammarHit[] };
    return data.hits || [];
  } catch {
    return null;
  }
}

/**
 * Lint a chunk of text via the server's LanguageTool proxy. Part of the
 * plugin API surface (src/plugins/host/PluginHost.tsx, src/plugins/api/index.ts)
 * so its non-nullable `Promise<GrammarHit[]>` signature must stay as-is;
 * callers that need to distinguish failure from "no issues" (Grammar.ts's
 * cache) should use `lintTextOrNull` instead.
 */
export async function lintText(text: string): Promise<GrammarHit[]> {
  return (await lintTextOrNull(text)) ?? [];
}
