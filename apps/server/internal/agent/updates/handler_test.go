/*
===========================================================================

handler_test.go - tests for the desktop application release routes

Verifies that /updates/app/ serves latest.json and the signed artifacts it
names from updates/app/, and refuses nested, hidden and traversal paths.

===========================================================================
*/
package updates

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

/*
================
TestAppReleaseRoutes
================
*/
func TestAppReleaseRoutes(t *testing.T) {
	updatesDir := t.TempDir()
	appDir := filepath.Join(updatesDir, AppReleaseDirName)
	manifest := `{"version":"1.150.1","platforms":{}}`
	writeFile(t, filepath.Join(appDir, AppReleaseManifestName), manifest)
	writeFile(t, filepath.Join(appDir, "Silkroad-1.150.1.exe"), "signed exe bytes")
	writeFile(t, filepath.Join(appDir, ".secret"), "never served")

	handler, err := NewHandler(SyncConfig{UpdatesDir: updatesDir}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler.waitForSync()

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	rec := get("/updates/app/latest.json")
	if rec.Code != http.StatusOK || rec.Body.String() != manifest {
		t.Fatalf("latest.json = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("latest.json Content-Type = %q", got)
	}
	if rec := get("/updates/app/Silkroad-1.150.1.exe"); rec.Code != http.StatusOK || rec.Body.String() != "signed exe bytes" {
		t.Fatalf("artifact = %d %q", rec.Code, rec.Body.String())
	}
	for _, blocked := range []string{
		"/updates/app/",
		"/updates/app/.secret",
		"/updates/app/../patches.json",
		"/updates/app/sub/latest.json",
		"/updates/app/missing.exe",
	} {
		if rec := get(blocked); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", blocked, rec.Code)
		}
	}
}
