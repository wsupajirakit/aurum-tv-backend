package logs

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"aurumtv/internal/platform/clock"
)

func newService(t *testing.T) (*Service, string) {
	dir := t.TempDir()
	return NewService(dir, true, 3, clock.System{}, slog.New(slog.NewTextHandler(io.Discard, nil))), dir
}

func TestAppendCapsFieldsAndPersists(t *testing.T) {
	s, _ := newService(t)
	r := s.Append(Input{"level": "warn", "message": strings.Repeat("m", 5000), "details": map[string]any{"big": strings.Repeat("d", 20000)}}, "1.2.3.4")
	if !r.Logged || len(r.Entry.Message) > maxLen["message"] || len(r.Entry.Details) > maxDetailsBytes+64 {
		t.Fatalf("caps not applied: msg=%d details=%d", len(r.Entry.Message), len(r.Entry.Details))
	}
	s.Flush()
	page, err := s.Get(Query{})
	if err != nil || page.Total != 1 || page.Logs[0].ClientIP != "1.2.3.4" {
		t.Fatalf("%v %+v", err, page)
	}
	if _, err := os.Stat(s.file(s.Today())); err != nil {
		t.Fatal("not persisted")
	}
}

func TestBatchLimitAndDisabled(t *testing.T) {
	s, _ := newService(t)
	items := []any{map[string]any{"message": "1"}, map[string]any{"message": "2"}, "not-an-object", map[string]any{"message": "4"}, map[string]any{}}
	r := s.AppendBatch(items, "")
	if *r.Count != 2 || r.Dropped != 2 {
		t.Fatalf("count=%d dropped=%d", *r.Count, r.Dropped)
	}
	s.SetEnabled(false)
	if s.Append(Input{"message": "x"}, "").Logged {
		t.Fatal("disabled logging must not store")
	}
}

func TestDayFileIsBounded(t *testing.T) {
	s, _ := newService(t)
	for i := 0; i < maxEntriesPerDay+10; i++ {
		s.Append(Input{"message": "x"}, "")
	}
	page, _ := s.Get(Query{Limit: "1"})
	if page.Total != maxEntriesPerDay {
		t.Fatalf("total %d", page.Total)
	}
	s.Flush()
}

func TestDatesRejectPathTricks(t *testing.T) {
	s, _ := newService(t)
	for _, d := range []string{"../../etc/passwd", "2026-1-1", "2026-01-01/../x", ""} {
		if _, err := s.Delete(d); err == nil {
			t.Errorf("%q accepted", d)
		}
	}
	if _, err := s.Get(Query{Date: "../x"}); err == nil {
		t.Error("Get accepted a path")
	}
}

func TestFilterAndPaging(t *testing.T) {
	s, _ := newService(t)
	for _, m := range []Input{{"level": "error", "tag": "Player", "message": "boom"}, {"level": "info", "tag": "UI", "message": "ok"}, {"level": "error", "tag": "Net", "message": "down", "channelId": "c1"}} {
		s.Append(m, "")
	}
	if p, _ := s.Get(Query{Level: "ERROR"}); p.Total != 2 || p.Logs[0].Message != "down" {
		t.Fatal("level filter / newest first")
	}
	if p, _ := s.Get(Query{ChannelID: "c1"}); p.Total != 1 {
		t.Fatal("channel filter")
	}
	if p, _ := s.Get(Query{Limit: "1", Offset: "1", Order: "asc"}); p.Count != 1 || p.Logs[0].Message != "ok" {
		t.Fatal("paging")
	}
	s.Flush()
}
