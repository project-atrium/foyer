// Package version holds Foyer's version, which is always Loge's: one number
// for the pair, so whoever runs them sees at once that they belong together.
// The app's `npm run release` moves both, and refuses when they differ.
package version

// Version is YEAR.MONTH.BUILD. BUILD counts every release and never goes down.
const Version = "2026.10.1"

// Protocol is the protocol Loge's Foyer adapters speak, as `@loge/api`'s
// FOYER_PROTOCOL states it. It rises when a change would leave an adapter
// that speaks the one before unable to read an answer.
const Protocol = 1
