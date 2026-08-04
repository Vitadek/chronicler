// Async feature bundle for LazyMotion (App.tsx). Split into its own module so
// `import('./motionFeatures')` fetches the Motion runtime in a lazy chunk
// instead of the eager entry chunk. No core transition relies on layout
// projection, so the smaller domAnimation feature set is sufficient.
import { domAnimation } from 'motion/react';

export default domAnimation;
