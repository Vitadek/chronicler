import { authFetch } from './authService';

export interface ManuscriptArchiveImportItem {
  sourceId: string;
  id: string;
  title: string;
  copied: boolean;
}

export interface ManuscriptArchiveImportResult {
  imported: number;
  renamed: number;
  covers: number;
  manuscripts: ManuscriptArchiveImportItem[];
}

async function archiveError(res: Response, fallback: string): Promise<Error> {
  try {
    const body = await res.json() as { error?: unknown };
    if (typeof body.error === 'string' && body.error.trim()) return new Error(body.error);
  } catch {
    // A proxy's plain-text/HTML error should not replace the useful fallback.
  }
  return new Error(fallback);
}

function attachmentFilename(header: string | null): string {
  const matched = /filename="?([^";]+)"?/i.exec(header || '')?.[1];
  return matched && /^[A-Za-z0-9_.-]+$/.test(matched)
    ? matched
    : `chronicler-manuscripts-${new Date().toISOString().slice(0, 10)}.chron`;
}

export const manuscriptArchiveService = {
  async exportLibrary(): Promise<void> {
    const res = await authFetch('/api/manuscripts/archive/export');
    if (!res.ok) throw await archiveError(res, 'Failed to export the manuscript archive');
    const blob = await res.blob();
    const { saveAs } = await import('file-saver');
    saveAs(blob, attachmentFilename(res.headers.get('content-disposition')));
  },

  async importLibrary(file: File): Promise<ManuscriptArchiveImportResult> {
    const res = await authFetch('/api/manuscripts/archive/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/vnd.chronicler.manuscripts+zip' },
      body: file,
    });
    if (!res.ok) throw await archiveError(res, 'Failed to import the manuscript archive');
    const result = await res.json() as ManuscriptArchiveImportResult;
    if (!Number.isInteger(result.imported) || !Array.isArray(result.manuscripts)) {
      throw new Error('The server returned an invalid import result');
    }
    return result;
  },
};
