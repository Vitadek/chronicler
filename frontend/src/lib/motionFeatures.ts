// Async feature bundle for LazyMotion (App.tsx). Split into its own module so
// `import('./motionFeatures')` fetches the ~130K motion runtime in a lazy
// chunk instead of the eager entry chunk. domMax (not domAnimation) because
// LibraryView's manuscript cards use layoutId shared-layout projection, which
// domAnimation doesn't include.
import { domMax } from 'motion/react';

export default domMax;
