// Package version holds Foyer's version, which is always Loge's: one number
// for the pair, so whoever runs them sees at once that they belong together.
// The app's `npm run release` moves both, and refuses when they differ.
package version

// Version is YEAR.MONTH.BUILD. BUILD counts every release and never goes down.
const Version = "2026.10.1"
