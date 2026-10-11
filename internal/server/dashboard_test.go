package server_test

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/ui"
)

func TestTheDashboardWearsFoyersIcon(t *testing.T) {
	h := start(t, options{})
	status, _, script := h.fetch("/_/extensions.js")
	if status != http.StatusOK || !bytes.Contains(script, []byte("./extensions/foyer/icon.png")) {
		t.Fatalf("extensions.js: %d %q", status, script)
	}
	status, header, icon := h.fetch("/_/extensions/foyer/icon.png")
	if status != http.StatusOK || header.Get("Content-Type") != "image/png" || !bytes.HasPrefix(icon, []byte("\x89PNG")) {
		t.Fatalf("icon: %d %q, %d bytes", status, header.Get("Content-Type"), len(icon))
	}
}

// Outside --dev PocketBase lets a browser keep /_/ for two weeks: after an
// upgrade, last version's pages would call this version's routes.
func TestTheDashboardsPagesAreNeverKeptStale(t *testing.T) {
	h := start(t, options{})
	for path, kind := range map[string]string{
		"/_/extensions.js":                 "javascript",
		"/_/extensions/foyer/library.js":   "javascript",
		"/_/extensions/foyer/adapters.js":  "javascript",
		"/_/extensions/foyer/household.js": "javascript",
		"/_/extensions/foyer/common.js":    "javascript",
		"/_/extensions/foyer/foyer.css":    "text/css",
	} {
		status, header, body := h.fetch(path)
		if status != http.StatusOK || len(body) == 0 {
			t.Errorf("%s: %d, %d bytes", path, status, len(body))
		}
		if !strings.Contains(header.Get("Content-Type"), kind) {
			t.Errorf("%s: served as %q", path, header.Get("Content-Type"))
		}
		if header.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: Cache-Control %q", path, header.Get("Cache-Control"))
		}
	}
	// The rest of the dashboard is PocketBase's to cache.
	if _, header, _ := h.fetch("/_/"); header.Get("Cache-Control") == "no-cache" {
		t.Error("the whole dashboard went uncached")
	}
}

// main.js is pasted into a function body, so it may neither import nor
// export; each page it loads must export the function it names.
func TestMainLoadsPagesThatExist(t *testing.T) {
	main := readUI(t, "main.js")
	if regexp.MustCompile(`(?m)^\s*(import|export)\s`).Match(main) {
		t.Error("main.js imports or exports: PocketBase pastes it into a function")
	}
	pages := regexp.MustCompile(`page\('([\w.-]+)', '(\w+)'\)`).FindAllSubmatch(main, -1)
	if len(pages) != 3 {
		t.Fatalf("main.js loads %d pages", len(pages))
	}
	for _, p := range pages {
		file, name := string(p[1]), string(p[2])
		if !bytes.Contains(readUI(t, file), []byte("export function "+name+"(")) {
			t.Errorf("%s does not export %s", file, name)
		}
	}
}

// PocketBase marks UI extensions experimental and documents none of what its
// dashboard offers them. Should it rename anything Foyer's pages use, this
// fails, instead of a page quietly breaking.
func TestTheDashboardStillHasEverythingFoyerUses(t *testing.T) {
	scripts, err := fs.Glob(ui.DistDirFS, "assets/*.js")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("PocketBase's dashboard has no scripts: %v %v", scripts, err)
	}
	var bundle []byte
	for _, name := range scripts {
		code, err := fs.ReadFile(ui.DistDirFS, name)
		if err != nil {
			t.Fatal(err)
		}
		bundle = append(bundle, code...)
	}
	if !bytes.Contains(bundle, []byte("/_/extensions.js")) {
		t.Error("PocketBase's dashboard no longer loads /_/extensions.js")
	}

	// Every app.… chain the pages use, PocketBase's own code uses too — but
	// app.pb, the JS SDK, which is documented, and versioned on its own.
	files, err := filepath.Glob(filepath.Join(uiDir(t), "*.js"))
	if err != nil || len(files) < 3 {
		t.Fatalf("Foyer's pages: %v %v", files, err)
	}
	chain := regexp.MustCompile(`\bapp(?:\.[A-Za-z_$][\w$]*)+`)
	var used []string
	for _, file := range files {
		code, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range chain.FindAll(code, -1) {
			used = append(used, string(m))
		}
	}
	slices.Sort(used)
	used = slices.Compact(used)
	for _, name := range used {
		if strings.HasPrefix(name, "app.pb.") {
			name = "app.pb."
		}
		if !bytes.Contains(bundle, []byte(name)) {
			t.Errorf("PocketBase's dashboard no longer has %s", name)
		}
	}
	for _, want := range []string{"app.store.favicon", "app.routes.superuserOnly", "app.components.pageSidebar"} {
		if !slices.Contains(used, want) {
			t.Errorf("the pages no longer use %s: is this test still reading them?", want)
		}
	}
}

func uiDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "dashboard", "ui"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func readUI(t *testing.T, name string) []byte {
	t.Helper()
	code, err := os.ReadFile(filepath.Join(uiDir(t), name))
	if err != nil {
		t.Fatal(err)
	}
	return code
}
