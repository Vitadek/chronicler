// Dump the automatic pre-restore backup database as JSON for
// orchestrator/assert-restore-artifacts.mjs to validate.
//
// Ported from an inline `node -` heredoc that ran INSIDE the chronicle
// container against better-sqlite3. The Go server image has neither Node nor
// better-sqlite3, so this now runs in the runner container instead, reading
// the backup copied out to /artifacts. The SQL and the emitted JSON shape are
// unchanged, so the assertions it feeds still check exactly what they did
// before — only the sqlite binding and the host container differ.
//
// node:sqlite is Node 22's built-in binding; its prepare/get/all/close surface
// matches better-sqlite3 closely. PRAGMA queries still use prepare/get because
// DatabaseSync deliberately does not expose better-sqlite3's pragma helper.
import { DatabaseSync } from 'node:sqlite';

const BACKUP_PATH = process.env.BACKUP_PATH;
const ALICE_ID = process.env.ALICE_ID;
if (!BACKUP_PATH || !ALICE_ID) {
  console.error('inspect-backup: BACKUP_PATH and ALICE_ID are required');
  process.exit(1);
}
const db = new DatabaseSync(BACKUP_PATH, { readOnly: true });
const alice = ALICE_ID;
const one = (sql, ...args) => db.prepare(sql).get(...args);
const count = (table) => one(`SELECT COUNT(*) AS n FROM ${table}`).n;
const authoritativeRevisions = [];
for (const row of db.prepare(`
  SELECT id, revision, deleted_at FROM manuscripts WHERE user_id = ? ORDER BY id
`).all(alice)) {
  authoritativeRevisions.push({
    entity: 'manuscript', id: row.id,
    operation: row.deleted_at === null ? 'upsert' : 'delete', revision: row.revision,
  });
}
for (const row of db.prepare(`
  SELECT manuscript_id, id, revision, deleted_at
  FROM chapters WHERE user_id = ? ORDER BY manuscript_id, id
`).all(alice)) {
  authoritativeRevisions.push({
    entity: 'chapter', manuscriptId: row.manuscript_id, id: row.id,
    operation: row.deleted_at === null ? 'upsert' : 'delete', revision: row.revision,
  });
}
for (const row of db.prepare('SELECT revision FROM profiles WHERE user_id = ?').all(alice)) {
  authoritativeRevisions.push({ entity: 'profile', id: 'profile', operation: 'upsert', revision: row.revision });
}
authoritativeRevisions.sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
const settingsRow = one('SELECT content FROM storage_blobs WHERE key = ?', `settings/${alice}`);
const durableManuscript = one(
  'SELECT data, revision FROM manuscripts WHERE user_id = ? AND id = ?', alice, 'formal_durable',
);
const durableChapter = one(`
  SELECT content, revision FROM chapters
  WHERE user_id = ? AND manuscript_id = ? AND id = ?
`, alice, 'formal_durable', 'collab');
const deletedManuscript = one(
  'SELECT data FROM manuscripts WHERE user_id = ? AND id = ?', alice, 'formal_restore_deleted',
);
const deletedChapter = one(`
  SELECT title, content, position FROM chapters
  WHERE user_id = ? AND manuscript_id = ? AND id = ?
`, alice, 'formal_restore_deleted', 'secret');
const outageManuscript = one(`
  SELECT data, deleted_at, revision FROM manuscripts WHERE user_id = ? AND id = ?
`, alice, 'formal_outage');
const outageChapter = one(`
  SELECT title, content, position, deleted_at, revision FROM chapters
  WHERE user_id = ? AND manuscript_id = ? AND id = ?
`, alice, 'formal_outage', 'offline');
const blobs = db.prepare(`
  SELECT key, lower(hex(content)) AS hex FROM storage_blobs ORDER BY key
`).all();
const outbox = db.prepare(`
  SELECT key, operation, dead_letter AS deadLetter
  FROM storage_replication_outbox ORDER BY key
`).all();
const result = {
  backupPath: process.env.BACKUP_PATH,
  integrity: one('PRAGMA integrity_check').integrity_check,
  epoch: one("SELECT v FROM kv WHERE k = 'sync:history-epoch:v2'").v,
  authoritativeRevisions,
  settings: JSON.parse(Buffer.from(settingsRow.content).toString('utf8')),
  durable: {
    title: JSON.parse(durableManuscript.data).title,
    manuscriptRevision: durableManuscript.revision,
    chapterContent: durableChapter.content,
    chapterRevision: durableChapter.revision,
  },
  deleted: {
    manuscriptData: deletedManuscript.data,
    chapterTitle: deletedChapter.title,
    chapterContent: deletedChapter.content,
    chapterPosition: deletedChapter.position,
    collaborationRows: one(
      'SELECT COUNT(*) AS n FROM ydocs WHERE name = ? OR name = ?',
      `${encodeURIComponent(alice)}/formal_restore_deleted:secret`,
      'formal_restore_deleted:secret',
    ).n,
    preCollaborationRows: one(`
      SELECT COUNT(*) AS n FROM chapter_pre_collab
      WHERE user_id = ? AND manuscript_id = ? AND chapter_id = ?
    `, alice, 'formal_restore_deleted', 'secret').n,
  },
  outage: {
    manuscriptData: outageManuscript.data,
    manuscriptDeletedAt: outageManuscript.deleted_at,
    manuscriptRevision: outageManuscript.revision,
    chapterTitle: outageChapter.title,
    chapterContent: outageChapter.content,
    chapterPosition: outageChapter.position,
    chapterDeletedAt: outageChapter.deleted_at,
    chapterRevision: outageChapter.revision,
  },
  profile: JSON.parse(one('SELECT data FROM profiles WHERE user_id = ?', alice).data),
  blobs,
  outbox,
  counts: {
    users: count('users'),
    manuscripts: count('manuscripts'),
    chapters: count('chapters'),
    profiles: count('profiles'),
    storageBlobs: count('storage_blobs'),
    replicaManifest: count('storage_replica_manifest'),
    replicaOutbox: count('storage_replication_outbox'),
  },
};
db.close();
process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
