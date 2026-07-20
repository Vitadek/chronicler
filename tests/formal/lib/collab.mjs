import { HocuspocusProvider, HocuspocusProviderWebsocket } from '@hocuspocus/provider';
import WebSocket from 'ws';
import * as Y from 'yjs';
import { authHeaders, baseUrl } from './api.mjs';
import { eventually } from './harness.mjs';

const wsUrl = baseUrl.replace(/^http/, 'ws') + '/collab';

// onSocketError is invoked when the underlying WebSocket fails, including a
// failed HTTP upgrade.
//
// The Go server refuses an unauthorized document BEFORE upgrading, answering
// 401/404 to the handshake. hocuspocus instead completes the upgrade and then
// sends an auth-denied message, which is what onAuthenticationFailed below
// observes. Both are legitimate denials — refusing pre-upgrade is arguably the
// stricter of the two — but `ws` surfaces the former as an 'error' event, and
// with no listener attached it re-throws and takes the entire run down with an
// unhandled ErrorEvent instead of failing one test.
//
// Recording it lets expectDenied see the denial promptly. This does not weaken
// anything: the assertion is still "the connection was refused".
function polyfillFor(user, includeHeaders = true, onSocketError = () => {}) {
  const headers = includeHeaders ? authHeaders(user) : {};
  return class FormalWebSocket extends WebSocket {
    constructor(url) {
      super(url, { headers });
      this.on('error', (error) => onSocketError(error));
    }
  };
}

export async function connectDocument({ user, documentName, includeHeaders = true }) {
  const document = new Y.Doc();
  let authFailure = null;
  let socketError = null;
  const socket = new HocuspocusProviderWebsocket({
    url: wsUrl,
    WebSocketPolyfill: polyfillFor(user, includeHeaders, (error) => {
      socketError = error?.message || 'websocket connection refused';
    }),
    // hocuspocus wraps the socket and surfaces failures through its own
    // callbacks, emitting an ErrorEvent that becomes an unhandled rejection if
    // nothing is listening — which aborted the entire run rather than failing
    // one test. Recording it here is the same denial signal as the raw socket
    // 'error' above.
    onError: (event) => {
      socketError = socketError || event?.message || 'websocket error';
    },
    onClose: (event) => {
      if (!event?.event?.wasClean) {
        socketError = socketError || 'websocket closed before handshake';
      }
    },
    connect: true,
    maxAttempts: 1,
    timeout: 5_000,
    quiet: true,
  });
  const provider = new HocuspocusProvider({
    name: documentName,
    document,
    websocketProvider: socket,
    broadcast: false,
    preserveConnection: false,
    quiet: true,
    onAuthenticationFailed: ({ reason }) => {
      authFailure = reason || 'authentication failed';
    },
  });
  await eventually(() => provider.synced || authFailure || socketError, {
    timeoutMs: 10_000,
    intervalMs: 50,
    label: `collaboration handshake for ${documentName}`,
  });
  // A refused upgrade counts as an authentication failure for callers: both
  // mean "the server would not let this client have this document".
  return {
    document,
    provider,
    socket,
    authFailure: () => authFailure || socketError,
  };
}

export async function expectDenied(options) {
  const connection = await connectDocument(options).catch((error) => ({ error }));
  if (connection.error) return true;
  try {
    await eventually(
      () => connection.authFailure() || (!connection.provider.isConnected && !connection.provider.synced),
      { timeoutMs: 5_000, intervalMs: 50, label: 'collaboration rejection' },
    );
    return true;
  } finally {
    connection.provider.destroy();
    connection.socket.destroy();
    connection.document.destroy();
  }
}

export async function convergeMap(left, right, key, value) {
  left.document.getMap('formal').set(key, value);
  return eventually(() => right.document.getMap('formal').get(key) === value, {
    timeoutMs: 10_000,
    intervalMs: 50,
    label: 'two-client Yjs convergence',
  });
}

export function closeConnection(connection) {
  connection.provider.destroy();
  connection.socket.destroy();
  connection.document.destroy();
}
