package version

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The app beside this repository has this version: its `npm run release`
// moves both, and a release that moved only one fails here.
func TestTheAppHasTheSameVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "loge", "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("the app is not beside this repository")
	}
	if err != nil {
		t.Fatal(err)
	}
	var app struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	if app.Version != Version {
		t.Fatalf("the app is %s and Foyer %s: the app's npm run release moves both", app.Version, Version)
	}
}
