package source

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"aurumtv/internal/channel"
	"aurumtv/internal/config"
	"aurumtv/internal/hls"
	"aurumtv/internal/platform/clock"
)

var three = []channel.Source{{ID: "a", Priority: 0}, {ID: "b", Priority: 1}, {ID: "c", Priority: 2}}

func healthOf(m map[string]Health) func(string) Health {
	return func(id string) Health {
		if h, ok := m[id]; ok {
			return h
		}
		return EmptyHealth()
	}
}

var policy = Policy{PreferPrimary: true, RecoverThreshold: 2}

func TestChoose(t *testing.T) {
	up := func(oks int) Health { return Health{Status: StatusUp, ConsecutiveOks: oks} }
	down := Health{Status: StatusDown}
	cases := []struct {
		name    string
		h       map[string]Health
		current string
		p       Policy
		want    Decision
	}{
		{"current ok", map[string]Health{"a": up(1)}, "a", policy, Decision{"a", "current_ok"}},
		{"failover to first up", map[string]Health{"a": down, "b": down, "c": up(1)}, "a", policy, Decision{"c", "failover"}},
		{"never switch to an unproven source", map[string]Health{"a": down}, "a", policy, Decision{"a", "no_healthy_backup"}},
		{"recovery below threshold", map[string]Health{"a": up(1), "b": up(3)}, "b", policy, Decision{"b", "current_ok"}},
		{"primary recovered", map[string]Health{"a": up(2), "b": up(3)}, "b", policy, Decision{"a", "primary_recovered"}},
		{"preferPrimary off", map[string]Health{"a": up(5), "b": up(1)}, "b", Policy{RecoverThreshold: 2}, Decision{"b", "current_ok"}},
		{"all down keeps current", map[string]Health{"a": down, "b": down, "c": down}, "b", policy, Decision{"b", "no_healthy_backup"}},
		{"initial skips down", map[string]Health{"a": down}, "", policy, Decision{"b", "initial"}},
	}
	for _, c := range cases {
		if got := Choose(three, healthOf(c.h), c.current, c.p); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestHysteresis(t *testing.T) {
	th := Thresholds{Fail: 2, Recover: 2}
	ok := hls.Result{OK: true, CheckedAt: "t"}
	reason := "http_404"
	bad := hls.Result{Stage: "playlist", Reason: &reason, CheckedAt: "t"}
	steps := []struct {
		r    hls.Result
		want string
	}{{ok, StatusUp}, {bad, StatusUp}, {bad, StatusDown}, {ok, StatusDown}, {ok, StatusUp}}
	h := EmptyHealth()
	for i, s := range steps {
		h = Apply(h, s.r, i, th)
		if h.Status != s.want {
			t.Fatalf("step %d: %s want %s", i, h.Status, s.want)
		}
	}
}

func TestDue(t *testing.T) {
	s := Schedule{BackupEveryCycles: 3, DownBackoffMaxCycles: 8}
	at := func(c int) *int { return &c }
	if !Due(Health{LastProbeCycle: at(10)}, 11, true, s) || !Due(EmptyHealth(), 1, false, s) {
		t.Fatal("active and never-probed sources are always due")
	}
	if Due(Health{Status: StatusUp, LastProbeCycle: at(10)}, 12, false, s) || !Due(Health{Status: StatusUp, LastProbeCycle: at(10)}, 13, false, s) {
		t.Fatal("backup schedule")
	}
	down := Health{Status: StatusDown, ConsecutiveFails: 4, LastProbeCycle: at(10)}
	if Due(down, 17, false, s) || !Due(down, 18, false, s) {
		t.Fatal("exponential backoff")
	}
	if !Due(Health{Status: StatusDown, ConsecutiveFails: 50, LastProbeCycle: at(10)}, 18, false, s) {
		t.Fatal("backoff cap")
	}
}

// ---------- monitor against a real repository ----------

func newRepo(t *testing.T) *channel.Repository {
	dir := t.TempDir()
	paths := config.Paths{DataDir: dir, ChannelsFile: dir + "/c.json", BackupFile: dir + "/c.bak", TmpFile: dir + "/c.tmp"}
	r := channel.NewRepository(paths, nil, clock.System{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	return r
}

func addChannel(t *testing.T, r *channel.Repository, urls ...string) string {
	srcs := make([]channel.RawSource, len(urls))
	for i, u := range urls {
		srcs[i] = channel.RawSource{ID: []string{"main", "b1", "b2"}[i], URL: u}
	}
	ch, err := r.Create(channel.Raw{Name: "Heal", Sources: srcs})
	if err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

type fake struct {
	mu    sync.Mutex
	table map[string]bool
	calls []string
	gate  map[string]chan struct{}
}

func (f *fake) probe(_ context.Context, s channel.Source, _ bool) Outcome {
	f.mu.Lock()
	f.calls = append(f.calls, s.URL)
	ok, gate := f.table[s.URL], f.gate[s.URL]
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	r := hls.Result{OK: ok, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if !ok {
		reason := "http_404"
		r.Stage, r.Reason = "playlist", &reason
	}
	return Outcome{Result: r, ResolvedURL: s.URL}
}

func (f *fake) count(url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == url {
			n++
		}
	}
	return n
}

func newMonitor(t *testing.T, r *channel.Repository, f *fake) *Monitor {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewMonitor(r, NewStore(t.TempDir()+"/source-health.json", log), f.probe, MonitorOptions{Concurrency: 2, ConfirmDelay: time.Millisecond,
		ReportCooldown: time.Minute, Interval: time.Hour, Thresholds: Thresholds{Fail: 2, Recover: 2},
		Schedule: Schedule{BackupEveryCycles: 3, DownBackoffMaxCycles: 8}, Policy: policy}, log)
}

func TestFailoverAfterConfirmation(t *testing.T) {
	r := newRepo(t)
	id := addChannel(t, r, "https://a/main", "https://b/b1", "https://c/b2")
	f := &fake{table: map[string]bool{"https://b/b1": true, "https://c/b2": true}}
	m := newMonitor(t, r, f)
	before := r.Meta().UpdatedAt

	res := m.RunCycle(context.Background(), id, false)
	if len(res.Switches) != 1 || res.Switches[0].To != "b1" || res.Switches[0].Reason != "failover" {
		t.Fatalf("switches %+v", res.Switches)
	}
	ch := r.Get(id)
	if ch.URL != "https://b/b1" || ch.ActiveID() != "b1" || *r.Meta().UpdatedAt == *before {
		t.Fatalf("channel %+v", ch)
	}
	if f.count("https://a/main") != 2 {
		t.Fatal("the primary must be re-probed before switching")
	}
}

func TestHealthyChannelIsNotRewritten(t *testing.T) {
	r := newRepo(t)
	id := addChannel(t, r, "https://a/main", "https://b/b1")
	m := newMonitor(t, r, &fake{table: map[string]bool{"https://a/main": true, "https://b/b1": true}})
	stamp := *r.Meta().UpdatedAt
	m.RunCycle(context.Background(), id, true)
	m.RunCycle(context.Background(), id, true)
	if *r.Meta().UpdatedAt != stamp || r.Get(id).ActiveID() != "main" {
		t.Fatal("routine probes must not touch channel data")
	}
}

func TestSwitchBackToPrimary(t *testing.T) {
	r := newRepo(t)
	id := addChannel(t, r, "https://a/main", "https://b/b1")
	f := &fake{table: map[string]bool{"https://b/b1": true}}
	m := newMonitor(t, r, f)
	m.RunCycle(context.Background(), id, true)
	f.mu.Lock()
	f.table["https://a/main"] = true
	f.mu.Unlock()
	m.RunCycle(context.Background(), id, true)
	if r.Get(id).ActiveID() != "b1" {
		t.Fatal("one success is not enough")
	}
	m.RunCycle(context.Background(), id, true)
	if r.Get(id).ActiveID() != "main" {
		t.Fatal("primary should be back")
	}
}

func TestReportDuringARunningCycleIsNotDropped(t *testing.T) {
	r := newRepo(t)
	busy := addChannel(t, r, "https://busy/main")
	reported := addChannel(t, r, "https://reported/main")
	release := make(chan struct{})
	f := &fake{table: map[string]bool{"https://busy/main": true, "https://reported/main": true},
		gate: map[string]chan struct{}{"https://busy/main": release}}
	m := newMonitor(t, r, f)

	done := make(chan struct{})
	go func() { m.RunCycle(context.Background(), busy, true); close(done) }()
	time.Sleep(20 * time.Millisecond)
	if !m.Report(reported) {
		t.Fatal("report should be accepted")
	}
	time.Sleep(20 * time.Millisecond)
	if f.count("https://reported/main") != 0 {
		t.Fatal("must wait for the running cycle")
	}
	close(release)
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for f.count("https://reported/main") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.count("https://reported/main") == 0 {
		t.Fatal("reported channel was never probed")
	}
	if m.Report(reported) {
		t.Fatal("cooldown")
	}
}
