package ocap

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOpenMissionFallsBackToHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data/mission.json.gz" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"from":"http"}`)
	}))
	defer srv.Close()

	open := func(t *testing.T, dir string) string {
		t.Helper()
		c := NewClient(srv.URL, dir, "", time.Second, slog.New(slog.DiscardHandler))
		rc, err := c.OpenMission(context.Background(), "mission")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("local file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "mission.json.gz"), []byte(`{"from":"disk"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := open(t, dir); got != `{"from":"disk"}` {
			t.Errorf("got %s, want the local file", got)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if got := open(t, t.TempDir()); got != `{"from":"http"}` {
			t.Errorf("got %s, want the download", got)
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		// OCAP's WebSocket ingest writes recordings with mode 0600.
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("file modes do not restrict reading here")
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "mission.json.gz"), []byte(`{"from":"disk"}`), 0o000); err != nil {
			t.Fatal(err)
		}
		if got := open(t, dir); got != `{"from":"http"}` {
			t.Errorf("got %s, want the download", got)
		}
	})
}
