import assert from 'node:assert/strict';
import {
  sanitizeEpubChapterHtml,
  sanitizeHtmlExportBody,
  unwrapProofreadRequestSpans,
} from '../src/lib/exportSanitize';

const privateNote = 'Internal note > never publish';
const marked = `<p>Before <span title="Proofreader request: ${privateNote}" class="proofread-request-marker" data-proofread-note='${privateNote}' style="background:purple" data-proofread-request="request-1">marked <strong>prose</strong> <span class="voice" title="keep me">inside</span></span> after.</p>`;
const published = '<p>Before marked <strong>prose</strong> <span class="voice" title="keep me">inside</span> after.</p>';

assert.equal(unwrapProofreadRequestSpans(marked), published);

const nestedRequests = '<p><span data-proofread-request="outer">Outer <span data-proofread-request="inner"><em>inner</em></span> tail</span>.</p>';
assert.equal(unwrapProofreadRequestSpans(nestedRequests), '<p>Outer <em>inner</em> tail.</p>');

const partialLegacyMarker = '<p><span class="proofread-request-marker" data-proofread-note="private" title="private" style="color:red">Keep these words</span></p>';
assert.equal(unwrapProofreadRequestSpans(partialLegacyMarker), '<p>Keep these words</p>');

const ordinarySpan = '<p><span class="voice" title="narration" style="font-style:italic">Ordinary prose</span></p>';
assert.equal(unwrapProofreadRequestSpans(ordinarySpan), ordinarySpan, 'unrelated inline spans must remain byte-for-byte intact');
const markerWordsInOrdinaryAttributes = '<p><span title="Discuss data-proofread-note and proofread-request-marker">Still ordinary</span></p>';
assert.equal(
  unwrapProofreadRequestSpans(markerWordsInOrdinaryAttributes),
  markerWordsInOrdinaryAttributes,
  'marker-like words inside an unrelated attribute value must not remove the span',
);

for (const [path, sanitize] of [
  ['EPUB', sanitizeEpubChapterHtml],
  ['HTML/print/PDF', sanitizeHtmlExportBody],
] as const) {
  const output = sanitize(`${marked}<script>window.secret = true</script>`);
  assert.equal(output, published, `${path} must preserve the marked prose and nested formatting`);
  assert.doesNotMatch(output, /proofread-request|data-proofread-note|Internal note|background:purple/i, `${path} must not publish request metadata or styling`);
}

assert.equal(
  sanitizeEpubChapterHtml('<p>Line one<br>Line two</p>'),
  '<p>Line one<br/>Line two</p>',
  'EPUB-specific XHTML cleanup remains in place',
);

console.log('Proofread request markers are removed from EPUB and HTML/print publication paths.');
