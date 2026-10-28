// Package logs stores app logs in one JSON file per day (DATA_DIR/logs/logs-YYYY-MM-DD.json).
// Entries are buffered in memory and flushed atomically in small batches.
package logs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aurumtv/internal/httpx"
	"aurumtv/internal/platform/clock"
	"aurumtv/internal/platform/fsx"
)

const (
	filePrefix      = "logs-"
	fileSuffix      = ".json"
	flushDelay      = 80 * time.Millisecond
	defaultLimit    = 100
	maxLimit        = 1000
	maxDetailsBytes = 8 * 1024
	// maxEntriesPerDay bounds memory and disk per day file; the oldest entries are dropped first.
	maxEntriesPerDay = 20000
)

// Field length caps — a client cannot fill the disk with giant log entries.
var maxLen = map[string]int{"level": 16, "tag": 64, "state": 96, "message": 2000, "channelId": 128, "channelName": 160}

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Entry is one stored log line.
type Entry struct {
	ID          string          `json:"id"`
	Timestamp   string          `json:"timestamp"`
	Level       string          `json:"level"`
	Tag         string          `json:"tag"`
	State       string          `json:"state"`
	Message     string          `json:"message"`
	Details     json.RawMessage `json:"details"`
	ChannelID   *string         `json:"channelId"`
	ChannelName *string         `json:"channelName"`
	ClientIP    string          `json:"clientIp"`
}

// Input is a log sent by the app (loosely typed on purpose).
type Input map[string]any

type Service struct {
	dir      string
	clock    clock.Clock
	enabled  atomic.Bool
	maxBatch int
	log      *slog.Logger

	mu      sync.Mutex
	byDate  map[string][]Entry
	dirty   map[string]bool
	timer   *time.Timer
	writeMu sync.Mutex
}

func NewService(dir string, enabled bool, maxBatch int, c clock.Clock, log *slog.Logger) *Service {
	s := &Service{dir: dir, clock: c, maxBatch: maxBatch, log: log, byDate: map[string][]Entry{}, dirty: map[string]bool{}}
	s.enabled.Store(enabled)
	return s
}

func (s *Service) Enabled() bool          { return s.enabled.Load() }
func (s *Service) SetEnabled(v bool) bool { s.enabled.Store(v); return v }
func (s *Service) Today() string          { return s.clock.Now().Local().Format("2006-01-02") }

// ValidDate guards every path built from a date (no traversal such as "x/../../..").
func ValidDate(d string) error {
	if !datePattern.MatchString(d) {
		return httpx.NewError(http.StatusBadRequest, "invalid_date", "date must be YYYY-MM-DD")
	}
	return nil
}

func (s *Service) file(date string) string { return filepath.Join(s.dir, filePrefix+date+fileSuffix) }

// load returns the entries of a date (from memory, else from disk). Caller holds mu.
func (s *Service) loadLocked(date string) []Entry {
	if list, ok := s.byDate[date]; ok {
		return list
	}
	var list []Entry
	if data, err := os.ReadFile(s.file(date)); err == nil {
		_ = json.Unmarshal(data, &list)
	}
	if list == nil {
		list = []Entry{}
	}
	s.byDate[date] = list
	return list
}

func clip(v any, max int) string {
	var str string
	switch t := v.(type) {
	case nil:
		str = ""
	case string:
		str = t
	default:
		b, _ := json.Marshal(t)
		str = strings.Trim(string(b), `"`)
	}
	str = strings.TrimSpace(str)
	if r := []rune(str); len(r) > max {
		return string(r[:max])
	}
	return str
}

func orDefault(v any, d string) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	if v == nil || v == false || v == "" {
		return d
	}
	return v
}

func safeDetails(v any) json.RawMessage {
	obj, ok := v.(map[string]any)
	if !ok {
		if arr, isArr := v.([]any); isArr {
			b, _ := json.Marshal(arr)
			if len(b) > maxDetailsBytes {
				return json.RawMessage(fmt.Sprintf(`{"truncated":true,"originalBytes":%d}`, len(b)))
			}
			return b
		}
		return json.RawMessage(`{}`)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return json.RawMessage(`{"unserializable":true}`)
	}
	if len(b) > maxDetailsBytes {
		return json.RawMessage(fmt.Sprintf(`{"truncated":true,"originalBytes":%d}`, len(b)))
	}
	return b
}

func optional(v any, max int) *string {
	if v == nil || v == "" || v == false {
		return nil
	}
	s := clip(v, max)
	return &s
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) build(in Input, ip string, now time.Time) Entry {
	return Entry{
		ID:          "log_" + strconv.FormatInt(now.UnixMilli(), 10) + "_" + randomHex(4),
		Timestamp:   now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Level:       strings.ToLower(clip(orDefault(in["level"], "info"), maxLen["level"])),
		Tag:         clip(orDefault(in["tag"], "App"), maxLen["tag"]),
		State:       strings.ToUpper(clip(orDefault(in["state"], "UNKNOWN"), maxLen["state"])),
		Message:     clip(in["message"], maxLen["message"]),
		Details:     safeDetails(in["details"]),
		ChannelID:   optional(in["channelId"], maxLen["channelId"]),
		ChannelName: optional(in["channelName"], maxLen["channelName"]),
		ClientIP:    ip,
	}
}

// AppendResult mirrors the API response.
type AppendResult struct {
	Logged  bool   `json:"logged"`
	Entry   *Entry `json:"entry,omitempty"`
	Count   *int   `json:"count,omitempty"`
	Dropped int    `json:"dropped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func (s *Service) Append(in Input, ip string) AppendResult {
	if !s.Enabled() {
		return AppendResult{Reason: "Logging is currently disabled"}
	}
	now := s.clock.Now()
	date := now.Local().Format("2006-01-02")
	e := s.build(in, ip, now)
	s.mu.Lock()
	list := append(s.loadLocked(date), e)
	if over := len(list) - maxEntriesPerDay; over > 0 {
		list = append([]Entry(nil), list[over:]...)
	}
	s.byDate[date] = list
	s.dirty[date] = true
	s.mu.Unlock()
	urgent := e.Level == "error" || strings.Contains(e.State, "ERROR")
	s.scheduleFlush(urgent)
	return AppendResult{Logged: true, Entry: &e}
}

// AppendBatch keeps at most maxBatch entries and reports how many were dropped.
func (s *Service) AppendBatch(items []any, ip string) AppendResult {
	zero := 0
	if !s.Enabled() {
		return AppendResult{Count: &zero, Reason: "Logging is currently disabled"}
	}
	if len(items) == 0 {
		return AppendResult{Logged: true, Count: &zero}
	}
	accepted := items
	if len(accepted) > s.maxBatch {
		accepted = accepted[:s.maxBatch]
	}
	count := 0
	for _, item := range accepted {
		if in, ok := item.(map[string]any); ok && s.Append(in, ip).Logged {
			count++
		}
	}
	return AppendResult{Logged: true, Count: &count, Dropped: len(items) - len(accepted)}
}

func (s *Service) scheduleFlush(urgent bool) {
	if urgent {
		go s.Flush()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer == nil {
		s.timer = time.AfterFunc(flushDelay, func() {
			s.mu.Lock()
			s.timer = nil
			s.mu.Unlock()
			s.Flush()
		})
	}
}

// Flush writes every dirty date atomically.
func (s *Service) Flush() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	batch := map[string][]Entry{}
	for date := range s.dirty {
		batch[date] = append([]Entry(nil), s.byDate[date]...)
	}
	s.dirty = map[string]bool{}
	s.mu.Unlock()
	for date, list := range batch {
		if err := fsx.WriteJSON(s.file(date), list, fsx.WriteOptions{TmpFile: s.file(date) + ".tmp", Pretty: true}); err != nil {
			s.log.Error("writing log file failed", "date", date, "err", err)
		}
	}
}

// Query filters for Get.
type Query struct {
	Date, Level, Tag, State, Q, ChannelID, Limit, Offset, Order string
}

type Page struct {
	Date    string  `json:"date"`
	Enabled bool    `json:"enabled"`
	Total   int     `json:"total"`
	Count   int     `json:"count"`
	Offset  int     `json:"offset"`
	Limit   int     `json:"limit"`
	Logs    []Entry `json:"logs"`
}

func (s *Service) Get(q Query) (*Page, error) {
	date := q.Date
	if date == "" {
		date = s.Today()
	} else if err := ValidDate(date); err != nil {
		return nil, err
	}
	s.mu.Lock()
	all := append([]Entry(nil), s.loadLocked(date)...)
	s.mu.Unlock()

	var out []Entry
	for _, e := range all {
		if q.Level != "" && e.Level != strings.ToLower(q.Level) {
			continue
		}
		if q.Tag != "" && !strings.Contains(strings.ToLower(e.Tag), strings.ToLower(q.Tag)) {
			continue
		}
		if q.State != "" && !strings.Contains(e.State, strings.ToUpper(q.State)) {
			continue
		}
		if q.ChannelID != "" && (e.ChannelID == nil || *e.ChannelID != q.ChannelID) {
			continue
		}
		if q.Q != "" {
			needle := strings.ToLower(q.Q)
			hay := strings.ToLower(e.Message + "\n" + e.Tag + "\n" + e.State + "\n" + string(e.Details))
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		out = append(out, e)
	}
	if q.Order == "" || q.Order == "desc" {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	limit, err := strconv.Atoi(q.Limit)
	if err != nil || limit == 0 {
		limit = defaultLimit
	}
	limit = min(max(limit, 1), maxLimit)
	offset, _ := strconv.Atoi(q.Offset)
	offset = max(offset, 0)
	page := []Entry{}
	if offset < len(out) {
		page = out[offset:min(len(out), offset+limit)]
	}
	return &Page{Date: date, Enabled: s.Enabled(), Total: len(out), Count: len(page), Offset: offset, Limit: limit, Logs: page}, nil
}

type FileInfo struct {
	Date       string `json:"date"`
	Filename   string `json:"filename"`
	SizeBytes  int64  `json:"sizeBytes"`
	EntryCount int    `json:"entryCount"`
	UpdatedAt  string `json:"updatedAt"`
}

func (s *Service) Dates() []FileInfo {
	_ = os.MkdirAll(s.dir, fsx.DirMode)
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return []FileInfo{}
	}
	out := []FileInfo{}
	for _, f := range files {
		name := f.Name()
		if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		date := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
		if ValidDate(date) != nil {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		s.mu.Lock()
		count := len(s.loadLocked(date))
		s.mu.Unlock()
		out = append(out, FileInfo{Date: date, Filename: name, SizeBytes: info.Size(), EntryCount: count,
			UpdatedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Date > out[b].Date })
	return out
}

type DeleteResult struct {
	Deleted bool   `json:"deleted"`
	Date    string `json:"date"`
	Reason  string `json:"reason,omitempty"`
}

func (s *Service) Delete(date string) (*DeleteResult, error) {
	if err := ValidDate(date); err != nil {
		return nil, err
	}
	s.mu.Lock()
	delete(s.byDate, date)
	delete(s.dirty, date)
	s.mu.Unlock()
	if err := os.Remove(s.file(date)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DeleteResult{Date: date, Reason: "File not found"}, nil
		}
		return nil, err
	}
	return &DeleteResult{Deleted: true, Date: date}, nil
}
