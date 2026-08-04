import {StrictMode} from 'react';
import {createRoot} from 'react-dom/client';
import App from './App.tsx';
import { AuthGate } from './components/AuthGate.tsx';
import './index.css';
import './styles/checkers.css';
import { migrateLocalStorageKeys } from './lib/localStorageMigration';
import { hydrateSettingsFromServer } from './lib/settingsSync';
import { authService } from './services/authService';

// Migrate scribe_* keys to chronicle_* before any component reads them.
migrateLocalStorageKeys();

// Settings hydration, the public auth config, and an auth check against
// whatever token is already in localStorage are mutually independent — none
// of them depends on the others' results — so kick off all three here in
// parallel instead of serializing them (settings before mount, then config,
// then me() inside AuthGate). AuthGate consumes these via the `bootstrap`
// prop and shows its own "Signing in…" splash while they settle.
const bootstrap = {
  // Pull the user's server-stored preferences into localStorage BEFORE App
  // reads them — this is what keeps settings stable across updates, browser
  // evictions, and devices. Resolves fast (one small GET) and falls through
  // to local values offline or before login.
  settings: hydrateSettingsFromServer(),
  config: authService
    .getConfig()
    .then((c) => ({ ok: true as const, c }))
    .catch(() => ({ ok: false as const })),
  // undefined = server unreachable, null = server reachable but rejected the
  // stored token (or there wasn't one) — AuthGate branches on the distinction.
  me: authService.me().catch(() => undefined),
};

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AuthGate bootstrap={bootstrap}>
      <App />
    </AuthGate>
  </StrictMode>,
);
