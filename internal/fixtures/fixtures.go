// Package fixtures reads what Loge and Foyer must answer alike: the files in
// the app's `adapters/api/fixtures/foyer/`, which the app's own tests read
// too. Go cannot import `@loge/api`; these hold the two to one answer — how a
// title is keyed, what a thing is, the order of a page, each route's shape.
//
// Only tests import it. The app sits beside Foyer in the workspace; a
// checkout of Foyer alone has no app beside it, and the tests that read these
// say so and skip.
package fixtures

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Path is a fixture's place in the app beside this repository.
func Path(name string) string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "loge", "adapters", "api", "fixtures", "foyer", filepath.FromSlash(name))
}

// Load decodes a fixture into v, or skips the test where the app is not
// beside Foyer.
func Load(t testing.TB, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(Path(name))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("the app is not beside Foyer, so %s is not here: this is tested in the workspace", name)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}
