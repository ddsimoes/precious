package spa_test

import (
	"errors"
	"io/fs"
	"net/http"
	"testing"

	"precious/internal/web/spa"
	"precious/web"
)

// After `make ui`, the binary embeds the built index.html: it is served as
// the shell, references hashed assets that resolve with immutable caching,
// and has no inline script or style (task 1.10). Without a build there is
// nothing to check.
func TestEmbeddedBuiltUI(t *testing.T) {
	index, err := fs.ReadFile(web.Dist(), "index.html")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("web/dist/index.html is not built; run `make ui` to embed the UI")
	}
	if err != nil {
		t.Fatal(err)
	}
	h, err := spa.New(web.Dist())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/", "/map/12"} {
		assertShell(t, target, get(h, http.MethodGet, target), string(index))
	}
	if inline := inlineCode(string(index)); inline != "" {
		t.Errorf("built index.html has inline code: %s", inline)
	}
	refs := assetRef.FindAllStringSubmatch(string(index), -1)
	if len(refs) == 0 {
		t.Fatalf("built index.html references no /assets/ file:\n%s", index)
	}
	for _, m := range refs {
		rec := get(h, http.MethodGet, m[1])
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != spa.AssetCacheControl {
			t.Errorf("GET %s = %d, Cache-Control %q", m[1], rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}
