import { authFetch } from './authService';

/**
 * Cover art upload + fetch.
 *
 * Server validates the format via magic bytes, so the client only has to
 * pass the raw file. Allowed types: image/png, image/jpeg, image/webp.
 *
 * Cover image serving is auth-gated (the server scopes covers to the
 * authenticated user), so we can't use the filename URL directly in an
 * <img src=...>. Instead we fetch the bytes via authFetch, build a blob
 * URL, and hand that to the UI. The cache below keeps repeat lookups fast.
 */

const blobCache = new Map<string, Promise<string | null>>();

export async function uploadCover(manuscriptId: string, file: File): Promise<string> {
  const response = await authFetch(`/api/covers/${encodeURIComponent(manuscriptId)}`, {
    method: 'POST',
    headers: { 'Content-Type': file.type || 'application/octet-stream' },
    body: file,
  });
  if (!response.ok) {
    let msg = `Upload failed: ${response.status}`;
    try {
      const j = await response.json();
      if (j?.error) msg = j.error;
    } catch { /* */ }
    throw new Error(msg);
  }
  const data = await response.json();
  return data.coverArt as string;
}

export async function deleteCover(manuscriptId: string): Promise<void> {
  await authFetch(`/api/covers/${encodeURIComponent(manuscriptId)}`, {
    method: 'DELETE',
  });
}

/**
 * Fetch a cover image and return a blob URL suitable for <img src=...>.
 * Cached per filename so a re-render doesn't refetch.
 *
 * Returns null if the cover is missing or the request fails — callers can
 * fall back to a placeholder.
 */
export function loadCoverBlobUrl(filename: string): Promise<string | null> {
  const cached = blobCache.get(filename);
  if (cached) return cached;
  // Cache the in-flight promise (not just the resolved URL) so concurrent
  // callers for the same filename — React StrictMode's double-invoked
  // effects, or EditorView + CoverArtUpload mounting together — share one
  // fetch and one object URL instead of each creating (and orphaning) their
  // own blob URL.
  const promise = (async () => {
    try {
      const res = await authFetch(`/api/covers/${encodeURIComponent(filename)}`);
      if (!res.ok) return null;
      const blob = await res.blob();
      return URL.createObjectURL(blob);
    } catch {
      return null;
    }
  })().then((url) => {
    if (!url) {
      // Don't cache failures — a transient network error shouldn't
      // permanently deny this filename for the rest of the session.
      blobCache.delete(filename);
    }
    return url;
  });
  blobCache.set(filename, promise);
  return promise;
}

/** Invalidate the cache for one filename (e.g. after replacing the cover). */
export function clearCoverCache(filename?: string): void {
  if (filename) {
    const pending = blobCache.get(filename);
    if (pending) pending.then((u) => { if (u) URL.revokeObjectURL(u); });
    blobCache.delete(filename);
    clearThumbCacheFor(filename);
    return;
  }
  // Wipe everything.
  for (const pending of blobCache.values()) {
    pending.then((u) => { if (u) URL.revokeObjectURL(u); });
  }
  blobCache.clear();
  for (const pending of thumbCache.values()) {
    pending.then((u) => { if (u) URL.revokeObjectURL(u); });
  }
  thumbCache.clear();
}

/**
 * Separate cache for downscaled thumbnails, keyed by `${filename}@${maxPx}`.
 * Deliberately NOT the same Map as blobCache: EditorView's title-page cover
 * and epubExport both read blobCache by filename and must keep getting the
 * full-resolution image, so the downscaled thumbnail is never written there.
 */
const thumbCache = new Map<string, Promise<string | null>>();

function clearThumbCacheFor(filename: string): void {
  for (const [key, pending] of thumbCache) {
    if (key === filename || key.startsWith(`${filename}@`)) {
      pending.then((u) => { if (u) URL.revokeObjectURL(u); });
      thumbCache.delete(key);
    }
  }
}

/**
 * Fetch a cover image and return a small downscaled blob URL suitable for
 * list/grid thumbnails (e.g. LibraryView's CoverThumb). Downloads the same
 * full-size bytes as loadCoverBlobUrl but never retains or exposes the
 * full-size blob URL — only the downscaled canvas output is cached, and
 * only in thumbCache, so full-size consumers are unaffected.
 *
 * Returns null if the cover is missing, the request fails, or the browser
 * can't decode/downscale the image — callers can fall back to a placeholder.
 */
export function loadCoverThumbUrl(filename: string, maxPx = 128): Promise<string | null> {
  const cacheKey = `${filename}@${maxPx}`;
  const cached = thumbCache.get(cacheKey);
  if (cached) return cached;
  const promise = (async () => {
    try {
      const res = await authFetch(`/api/covers/${encodeURIComponent(filename)}`);
      if (!res.ok) return null;
      const blob = await res.blob();
      const bitmap = await createImageBitmap(blob);
      try {
        const scale = Math.min(1, maxPx / Math.max(bitmap.width, bitmap.height));
        const w = Math.max(1, Math.round(bitmap.width * scale));
        const h = Math.max(1, Math.round(bitmap.height * scale));
        const canvas = document.createElement('canvas');
        canvas.width = w;
        canvas.height = h;
        const ctx = canvas.getContext('2d');
        if (!ctx) return null;
        ctx.drawImage(bitmap, 0, 0, w, h);
        const thumbBlob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/webp', 0.85));
        if (!thumbBlob) return null;
        return URL.createObjectURL(thumbBlob);
      } finally {
        bitmap.close();
      }
    } catch {
      return null;
    }
  })().then((url) => {
    if (!url) thumbCache.delete(cacheKey);
    return url;
  });
  thumbCache.set(cacheKey, promise);
  return promise;
}
