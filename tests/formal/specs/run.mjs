// Surface the origin of an unhandled rejection instead of letting Node print a
// bare "#<ErrorEvent>" and abort the whole phase. Async WebSocket failures fire
// outside any test's promise chain, so without this a single stray socket error
// takes down a run that is otherwise passing — and hides which test caused it.
// Deliberately does NOT fail the phase. The denial tests intentionally provoke
// failed WebSocket handshakes and then destroy the provider, and hocuspocus
// emits a late ErrorEvent from that teardown with nothing left to await it.
// That is expected noise, not a regression — the TAP assertions below are the
// source of truth for whether the server behaved. Logging keeps it visible in
// case a genuinely unexpected rejection ever shows up here.
process.on('unhandledRejection', (reason) => {
  const detail = reason instanceof Error
    ? reason.stack
    : JSON.stringify(reason, Object.getOwnPropertyNames(Object(reason)));
  console.error(`# unhandled rejection (non-fatal, see comment in run.mjs): ${detail}`);
});

const phase = process.argv[2] || 'foundation';
const modules = {
  foundation: './foundation.mjs',
  outage: './outage.mjs',
  recovery: './recovery.mjs',
  durability: './durability.mjs',
  pre_restore: './pre-restore.mjs',
  post_restore: './post-restore.mjs',
};

if (!modules[phase]) throw new Error(`Unknown formal test phase: ${phase}`);
const { run } = await import(modules[phase]);
await run();

// WebSocket/AWS connection pools may retain idle keep-alive handles. Reports
// are already flushed at this point, so make each one-shot runner phase exit
// deterministically with its accumulated test status.
process.exit(process.exitCode || 0);
