import http from 'node:http';
import { LocalLinter } from 'harper.js';
import { binaryInlined } from 'harper.js/binaryInlined';

const port = Number(process.env.PORT || 8080);
const maxBytes = 256 * 1024;
const linter = new LocalLinter({ binary: binaryInlined });
await linter.setup();

const kindFor = (kind) => {
  if (kind === 'Spelling') return 'misspelling';
  if (kind === 'WordChoice' || kind === 'Malapropism' || kind === 'Eggcorn') return 'confusion';
  if (kind === 'Grammar' || kind === 'Agreement' || kind === 'WordOrder') return 'grammar';
  if (kind === 'Typo' || kind === 'Punctuation' || kind === 'Capitalization') return 'typographical';
  return 'style';
};

const json = (res, status, value) => {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(value));
};

const server = http.createServer((req, res) => {
  if (req.method === 'GET' && req.url === '/healthz') return json(res, 200, { ok: true });
  if (req.method === 'GET' && req.url === '/v1/capabilities') {
    return json(res, 200, { protocol: 1, engine: 'harper', version: '2.7.0', languages: ['en'], modes: ['standard'] });
  }
  if (req.method !== 'POST' || req.url !== '/v1/check') return json(res, 404, { error: 'Not found' });

  let size = 0;
  const chunks = [];
  req.on('data', (chunk) => {
    size += chunk.length;
    if (size > maxBytes) req.destroy();
    else chunks.push(chunk);
  });
  req.on('end', async () => {
    try {
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      if (typeof body.text !== 'string') return json(res, 400, { error: 'text must be a string' });
      const lints = await linter.lint(body.text, { language: 'plaintext', dedup: true });
      const findings = lints.map((lint) => {
        const span = lint.span();
        const lintKind = lint.lint_kind();
        const finding = {
          start: span.start,
          end: span.end,
          kind: kindFor(lintKind),
          message: lint.message(),
          replacements: lint.suggestions().slice(0, 5).map((suggestion) => suggestion.get_replacement_text()),
          ruleId: lintKind,
          category: 'Harper',
        };
        lint.free();
        return finding;
      });
      return json(res, 200, { findings });
    } catch {
      return json(res, 400, { error: 'Invalid request' });
    }
  });
});

server.listen(port, '0.0.0.0');
