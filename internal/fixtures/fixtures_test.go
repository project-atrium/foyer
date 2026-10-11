package fixtures_test

import (
	"testing"

	"foyer/internal/fixtures"
)

// Foyer alone, with no app beside it, skips what it cannot read — it does
// not fail.
func TestAFixtureThatIsNotThereSkips(t *testing.T) {
	read := false
	t.Run("missing", func(t *testing.T) {
		var v any
		fixtures.Load(t, "no-such-fixture.json", &v)
		read = true
	})
	if read {
		t.Fatal("a fixture that is not there did not skip")
	}
}
