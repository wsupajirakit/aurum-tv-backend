package channel

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aurumtv/internal/config"
	"aurumtv/internal/platform/clock"
)

var now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func TestNormalizeSources(t *testing.T) {
	got := NormalizeSources(Raw{Sources: []RawSource{
		{ID: "dup", URL: "https://b", Priority: ptr(5.0)},
		{ID: "dup", URL: "https://a", Priority: ptr(1.0)},
		{ID: "../bad id", URL: "https://c", Resolver: "evil"},
		{ID: "empty"},
		{ID: "dyn", Resolver: ResolverCh3Plus},
	}})
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	if got[0].URL != "https://a" || got[0].ID != "src-2" {
		t.Fatalf("lowest priority first and duplicate ids replaced: %+v", got[0])
	}
	for _, s := range got {
		if s.ID == "../bad id" || s.Resolver == "evil" {
			t.Fatalf("unsafe input kept: %+v", s)
		}
	}
	many := make([]RawSource, MaxSourcesPerChan+5)
	for i := range many {
		many[i] = RawSource{URL: "https://x"}
	}
	if n := len(NormalizeSources(Raw{Sources: many})); n != MaxSourcesPerChan {
		t.Fatalf("cap: %d", n)
	}
}

func TestNormalizeLegacyURLAndNumber(t *testing.T) {
	c := Normalize(Raw{Name: "  News  ", URL: "https://a/p.m3u8", Number: ptr(1.5)}, 7, now)
	if c.Name != "News" || c.Order != 7 || c.Number != nil || !c.Enabled || c.ID == "" {
		t.Fatalf("%+v", c)
	}
	if len(c.Sources) != 1 || c.ActiveID() != legacyPrimaryID || c.CreatedAt != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("%+v", c)
	}
}

func TestMergeKeepsURLAndSourcesConsistent(t *testing.T) {
	base := Normalize(Raw{Name: "A", Sources: []RawSource{{ID: "m", URL: "https://m"}, {ID: "b", URL: "https://b"}}}, 0, now)

	u := "https://m2"
	c := Normalize(Merge(base, Patch{URL: &u}), 0, now)
	if c.URL != u || c.Source("m").URL != u || c.Source("b").URL != "https://b" {
		t.Fatalf("url-only patch must move the active source: %+v", c)
	}
	srcs := []RawSource{{ID: "b", URL: "https://b"}, {ID: "z", URL: "https://z"}}
	c = Normalize(Merge(base, Patch{Sources: &srcs}), 0, now)
	if c.ActiveID() != "b" || c.URL != "https://b" {
		t.Fatalf("sources-only patch must recompute url: %+v", c)
	}
}

func TestMigrateV1(t *testing.T) {
	v1 := []Raw{
		{ID: "a", Name: "True4U 24", URL: "https://a", Order: ptr(0.0)},
		{ID: "b", Name: "Thai PBS", URL: "https://b", Order: ptr(1.0)},
		{ID: "c", Name: "Other", URL: "https://c", Order: ptr(2.0)},
		{ID: "dyn", Name: "3HD", URL: "https://d", Order: ptr(3.0)},
	}
	v, out, steps := Migrate(1, v1, []string{"dyn"})
	if v != SchemaVersion || len(steps) != 1 {
		t.Fatal(v, steps)
	}
	if out[2].ID != "dyn" || *out[2].Order != 2 || out[2].Sources[0].Resolver != ResolverCh3Plus {
		t.Fatalf("Channel 3 must land at position 3 with a resolver source: %+v", out[2])
	}
	if *out[0].Number != 24 || *out[1].Number != 3 {
		t.Fatal("number = last number in the name; Thai PBS = 3")
	}
	if v, _, steps := Migrate(2, out, nil); v != 2 || len(steps) != 0 {
		t.Fatal("v2 data is left alone")
	}
}

// ---------- repository ----------

func newRepo(t *testing.T, dir string) *Repository {
	t.Helper()
	paths := config.Paths{DataDir: dir, ChannelsFile: filepath.Join(dir, "channels.json"),
		BackupFile: filepath.Join(dir, "channels.bak.json"), TmpFile: filepath.Join(dir, "channels.tmp.json")}
	r := NewRepository(paths, nil, clock.System{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRepositorySeedPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	r := newRepo(t, dir)
	if r.Meta().Count == 0 {
		t.Fatal("empty data dir must start from the seed")
	}
	ch, err := r.Create(Raw{Name: "New", URL: "https://n"})
	if err != nil || ch.Order != r.Meta().Count-1 {
		t.Fatalf("%v %+v", err, ch)
	}
	again := newRepo(t, dir)
	if again.Get(ch.ID) == nil || again.Meta().Count != r.Meta().Count {
		t.Fatal("reload lost data")
	}
}

func TestRepositoryFallsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	r := newRepo(t, dir)
	_, _ = r.Create(Raw{Name: "Kept", URL: "https://k"})
	_, _ = r.Create(Raw{Name: "Kept 2", URL: "https://k2"}) // backup now holds "Kept"
	if err := os.WriteFile(filepath.Join(dir, "channels.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := newRepo(t, dir)
	found := false
	for _, c := range restored.List(Filter{}) {
		found = found || c.Name == "Kept"
	}
	if !found {
		t.Fatal("corrupt main file must restore from the backup")
	}
}

func TestNoChangeNoWrite(t *testing.T) {
	r := newRepo(t, t.TempDir())
	ch, _ := r.Create(Raw{Name: "A", URL: "https://a"})
	stamp := *r.Meta().UpdatedAt
	time.Sleep(2 * time.Millisecond)
	if _, changed, _ := r.UpdateSourceURL(ch.ID, ch.ActiveID(), "https://a"); changed {
		t.Fatal("same url reported as a change")
	}
	if _, changed, _ := r.SetActiveSource(ch.ID, ch.ActiveID(), "https://a"); changed {
		t.Fatal("same source reported as a change")
	}
	if *r.Meta().UpdatedAt != stamp {
		t.Fatal("updatedAt moved without a change — clients would reload for nothing")
	}
}

func TestReorderBulkRemoveAndEvents(t *testing.T) {
	r := newRepo(t, t.TempDir())
	events, cancel := r.Subscribe(8)
	defer cancel()
	created, err := r.BulkCreate([]Raw{
		{Name: "X", URL: "https://x"},
		{Name: "", URL: "https://skip"},
		{Name: "Y", Sources: []RawSource{{ID: "s", URL: "https://y"}}},
		{Name: "Z"},
	})
	if err != nil || len(created) != 2 {
		t.Fatalf("bulk: %v %d", err, len(created))
	}
	if e := <-events; e.Type != "bulk" {
		t.Fatal(e.Type)
	}
	list, _ := r.Reorder([]string{created[1].ID, created[0].ID})
	if list[0].ID != created[1].ID || list[1].ID != created[0].ID || list[2].Order != 2 {
		t.Fatal("reorder")
	}
	if _, err := r.Remove(created[0].ID); err != nil || r.Get(created[0].ID) != nil {
		t.Fatal("remove")
	}
	if missing, _ := r.Remove("nope"); missing != nil {
		t.Fatal("removing an unknown id is a no-op")
	}
}
