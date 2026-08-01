/**
 * Publication-only HTML cleanup.
 *
 * Proofread requests are durable editor marks, so their attributes and visual
 * treatment intentionally live in the manuscript HTML while the writer works.
 * They are private workflow metadata, however, and must never reach a reader.
 * These helpers unwrap only request-marker spans, preserving every child node
 * and all surrounding prose/formatting.
 */

interface ScannedTag {
  end: number;
  name: string;
  closing: boolean;
  selfClosing: boolean;
  raw: string;
}

/** Scan one HTML tag without treating `>` inside a quoted attribute as its end. */
function scanTag(html: string, start: number): ScannedTag | null {
  if (html[start] !== '<') return null;

  if (html.startsWith('<!--', start)) {
    const commentEnd = html.indexOf('-->', start + 4);
    const end = commentEnd < 0 ? html.length : commentEnd + 3;
    return { end, name: '', closing: false, selfClosing: false, raw: html.slice(start, end) };
  }

  let cursor = start + 1;
  let closing = false;
  if (html[cursor] === '/') {
    closing = true;
    cursor += 1;
  }
  while (/\s/.test(html[cursor] ?? '')) cursor += 1;

  const nameStart = cursor;
  while (/[A-Za-z0-9:-]/.test(html[cursor] ?? '')) cursor += 1;
  const name = html.slice(nameStart, cursor).toLowerCase();

  let quote: '"' | "'" | null = null;
  while (cursor < html.length) {
    const char = html[cursor];
    if (quote) {
      if (char === quote) quote = null;
    } else if (char === '"' || char === "'") {
      quote = char;
    } else if (char === '>') {
      const end = cursor + 1;
      const raw = html.slice(start, end);
      return {
        end,
        name,
        closing,
        selfClosing: !closing && /\/\s*>$/.test(raw),
        raw,
      };
    }
    cursor += 1;
  }

  return { end: html.length, name, closing, selfClosing: false, raw: html.slice(start) };
}

function isProofreadRequestTag(raw: string): boolean {
  let cursor = 1; // skip `<`
  while (/\s/.test(raw[cursor] ?? '')) cursor += 1;
  while (/[A-Za-z0-9:-]/.test(raw[cursor] ?? '')) cursor += 1; // `span`

  while (cursor < raw.length) {
    while (/\s/.test(raw[cursor] ?? '')) cursor += 1;
    if (raw[cursor] === '>' || raw[cursor] === '/' || cursor >= raw.length) break;

    const nameStart = cursor;
    while (!/[\s=/>]/.test(raw[cursor] ?? '>')) cursor += 1;
    const name = raw.slice(nameStart, cursor).toLowerCase();
    while (/\s/.test(raw[cursor] ?? '')) cursor += 1;

    let value = '';
    if (raw[cursor] === '=') {
      cursor += 1;
      while (/\s/.test(raw[cursor] ?? '')) cursor += 1;
      const quote = raw[cursor] === '"' || raw[cursor] === "'" ? raw[cursor] : null;
      if (quote) {
        cursor += 1;
        const valueStart = cursor;
        while (cursor < raw.length && raw[cursor] !== quote) cursor += 1;
        value = raw.slice(valueStart, cursor);
        if (raw[cursor] === quote) cursor += 1;
      } else {
        const valueStart = cursor;
        while (!/[\s/>]/.test(raw[cursor] ?? '>')) cursor += 1;
        value = raw.slice(valueStart, cursor);
      }
    }

    if (name === 'data-proofread-request' || name === 'data-proofread-note') return true;
    if (name === 'class' && value.split(/\s+/).some((token) => token.toLowerCase() === 'proofread-request-marker')) return true;
  }

  return false;
}

/**
 * Remove the marker element itself, not its contents. A small quote-aware tag
 * scanner is used instead of a `<span>.*?</span>` regex so nested inline spans
 * (comments, formatting, or overlapping request fragments) remain balanced.
 */
export function unwrapProofreadRequestSpans(html: string): string {
  if (!/proofread-request|data-proofread-note/i.test(html)) return html;

  const output: string[] = [];
  const spanStack: boolean[] = [];
  let cursor = 0;

  while (cursor < html.length) {
    const tagStart = html.indexOf('<', cursor);
    if (tagStart < 0) {
      output.push(html.slice(cursor));
      break;
    }
    output.push(html.slice(cursor, tagStart));

    const tag = scanTag(html, tagStart);
    if (!tag) {
      output.push('<');
      cursor = tagStart + 1;
      continue;
    }

    if (tag.name !== 'span') {
      output.push(tag.raw);
    } else if (tag.closing) {
      const wasRequest = spanStack.pop();
      // Preserve unmatched/malformed closing spans rather than altering prose.
      if (wasRequest !== true) output.push(tag.raw);
    } else {
      const isRequest = isProofreadRequestTag(tag.raw);
      if (!tag.selfClosing) spanStack.push(isRequest);
      if (!isRequest) output.push(tag.raw);
    }
    cursor = tag.end;
  }

  return output.join('');
}

function stripExecutableMarkup(html: string): string {
  return html
    .replace(/<script[\s\S]*?<\/script>/gi, '')
    .replace(/\son\w+="[^"]*"/gi, '');
}

/** XHTML-ish body cleanup used for every EPUB chapter. */
export function sanitizeEpubChapterHtml(html: string): string {
  return unwrapProofreadRequestSpans(stripExecutableMarkup(html))
    .replace(/<br\s*\/?>/gi, '<br/>')
    .replace(/<hr\s*\/?>/gi, '<hr/>')
    .replace(/<img([^>]*?)\/?>/gi, '<img$1/>')
    // Strip existing internal mark attributes so exported books do not carry
    // comment/audio editor metadata either.
    .replace(/\sdata-(comment|audio-token|from|to)="[^"]*"/g, '')
    .replace(/<span\s*(?:class="manuscript-(?:comment|audio)-marker"\s*)?>([^<]*)<\/span>/g, '$1');
}

/** Body cleanup used by standalone HTML and its browser print/PDF path. */
export function sanitizeHtmlExportBody(html: string): string {
  return unwrapProofreadRequestSpans(stripExecutableMarkup(html));
}
