package monitor

import (
	"bidmon/internal/evidence"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	cfg    Config
	db     *gorm.DB
	writer *sql.DB
	source *Source
	c      *Collector
	ctx    context.Context
}

func fixtureFor(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("RUN_GO_INTEGRATION") != "1" {
		t.Skip("Docker integration required")
	}
	cfg, e := LoadConfig()
	if e != nil {
		t.Fatal(e)
	}
	schema := "go_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	base, e := OpenStore(cfg.Database)
	if e != nil {
		t.Fatal(e)
	}
	if e = base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; e != nil {
		t.Fatal(e)
	}
	pool, _ := base.DB()
	pool.Close()
	u, e := url.Parse(cfg.Database)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	cfg.Database = u.String()
	cfg.Source = schema
	cfg.Poll = time.Second
	cfg.Queue = 3 * time.Second
	cfg.Stale = 3 * time.Second
	cfg.Grace = 2 * time.Second
	cfg.Page = 2
	db, e := OpenStore(cfg.Database)
	if e != nil {
		t.Fatal(e)
	}
	if e = Migrate(db); e != nil {
		t.Fatal(e)
	}
	writer, e := sql.Open("mysql", os.Getenv("GO_LAB_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	source, e := OpenSource(cfg.MySQL)
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{cfg: cfg, db: db, writer: writer, source: source, ctx: context.Background()}
	f.c = &Collector{Cfg: cfg, DB: db, Source: source}
	t.Cleanup(func() { pool, _ := db.DB(); pool.Close(); writer.Close(); source.DB.Close() })
	return f
}
func (f *fixture) seed(t *testing.T, status string, age time.Duration) Task {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := Task{ID: "go-" + uuid.NewString(), User: "synthetic-go", Status: status, Stage: "extract", Created: now.Add(-age), Updated: now.Add(-age)}
	_, e := f.writer.Exec("INSERT INTO async_tasks(task_id,user_id,biz_type,status,stage,created_at,updated_at) VALUES (?,?,'bidding',?,?,?,?)", task.ID, task.User, status, task.Stage, task.Created, task.Updated)
	if e != nil {
		t.Fatal(e)
	}
	return task
}
func (f *fixture) observe(t *testing.T, task Task, now time.Time) {
	t.Helper()
	if e := f.c.Observe(f.ctx, task, now); e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) events(t *testing.T, id string) []Event {
	t.Helper()
	var rows []Event
	if e := f.db.Where("task_id=?", id).Order("episode").Find(&rows).Error; e != nil {
		t.Fatal(e)
	}
	return rows
}
func TestIntegrationEventCloseRestartRecurrence(t *testing.T) {
	f := fixtureFor(t)
	task := f.seed(t, "failed", 0)
	now := time.Now().UTC()
	f.observe(t, task, now)
	f.observe(t, task, now.Add(time.Second))
	rows := f.events(t, task.ID)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	event, e := CloseEvent(f.db, f.cfg, rows[0].EventID, "这是实验故意制造的失败，不需要继续处理")
	if e != nil || event.Status != "closed_unresolved" {
		t.Fatal(e, event)
	}
	if _, e = CloseEvent(f.db, f.cfg, event.EventID, "重复关闭"); e != nil {
		t.Fatal(e)
	}
	f.observe(t, task, now.Add(2*time.Second))
	if len(f.events(t, task.ID)) != 1 {
		t.Fatal("reopened")
	}
	task.Status = "completed"
	task.Updated = now
	f.observe(t, task, now.Add(3*time.Second))
	f.observe(t, task, now.Add(4*time.Second))
	rows = f.events(t, task.ID)
	if rows[0].ConditionActive || rows[0].Status != "closed_unresolved" {
		t.Fatal("close rewritten")
	}
	task.Status = "failed"
	f.observe(t, task, now.Add(5*time.Second))
	rows = f.events(t, task.ID)
	failed := []Event{}
	for _, r := range rows {
		if r.RuleCode == "TASK_FAILED" {
			failed = append(failed, r)
		}
	}
	if len(failed) != 2 || failed[1].Episode != 2 {
		t.Fatal("recurrence missing")
	}
}
func TestIntegrationResultCandidates(t *testing.T) {
	for _, tc := range []struct {
		name, raw, rule string
		count           int
	}{{"empty", resultFixture(true), "RESULT_EMPTY_SUSPECT", 1}, {"present", resultFixture(false), "", 1}, {"invalid", "broken", "RESULT_STRUCTURE_INVALID", 1}, {"duplicate", resultFixture(false), "RESULT_STRUCTURE_INVALID", 2}, {"missing", "", "RESULT_MISSING", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureFor(t)
			task := f.seed(t, "completed", 5*time.Second)
			for i := 0; i < tc.count; i++ {
				if _, e := f.writer.Exec("INSERT INTO bidding_results(task_id,user_id,result_json,status) VALUES (?,?,?,'completed')", task.ID, task.User, tc.raw); e != nil {
					t.Fatal(e)
				}
			}
			now := time.Now()
			f.observe(t, task, now)
			if len(f.events(t, task.ID)) != 0 {
				t.Fatal("candidate opened without confirmation")
			}
			f.observe(t, task, now.Add(time.Second))
			rows := f.events(t, task.ID)
			if tc.rule == "" && len(rows) != 0 || tc.rule != "" && (len(rows) != 1 || rows[0].RuleCode != tc.rule) {
				t.Fatal(rows)
			}
		})
	}
}
func TestIntegrationSourceGapAndMissing(t *testing.T) {
	f := fixtureFor(t)
	task := f.seed(t, "pending", 5*time.Second)
	now := time.Now()
	f.observe(t, task, now)
	if e := f.c.Health(f.ctx, false, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	f.observe(t, task, now.Add(2*time.Second))
	if len(f.events(t, task.ID)) != 0 {
		t.Fatal("outage bridged")
	}
	f.observe(t, task, now.Add(3*time.Second))
	if len(f.events(t, task.ID)) != 1 {
		t.Fatal("missing candidate")
	}
	for i := 2; i <= 3; i++ {
		if e := f.c.Health(f.ctx, false, now.Add(time.Duration(i+3)*time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	if len(f.events(t, "__source__")) != 1 {
		t.Fatal("source failure not raised")
	}
	if e := f.c.Health(f.ctx, true, now.Add(7*time.Second)); e != nil {
		t.Fatal(e)
	}
	if e := f.c.Health(f.ctx, true, now.Add(8*time.Second)); e != nil {
		t.Fatal(e)
	}
	if f.events(t, "__source__")[0].Status != "resolved" {
		t.Fatal("source recovery missing")
	}
}
func TestIntegrationScanPagingAndReadOnly(t *testing.T) {
	f := fixtureFor(t)
	for i := 0; i < 5; i++ {
		f.seed(t, "failed", 0)
	}
	if e := f.c.Scan(f.ctx); e != nil {
		t.Fatal(e)
	}
	var cur Cursor
	if e := f.db.First(&cur, "key=?", f.c.cursor()).Error; e != nil || cur.HighWatermark == nil {
		t.Fatal(e)
	}
	before := *cur.HighWatermark
	bad := *f.source
	bad.DB.Close()
	if e := f.c.Scan(f.ctx); e == nil {
		t.Fatal("source outage ignored")
	}
	if e := f.db.First(&cur, "key=?", f.c.cursor()).Error; e != nil || !cur.HighWatermark.Equal(before) {
		t.Fatal("watermark changed on partial failure")
	}
	source, e := OpenSource(f.cfg.MySQL)
	if e != nil {
		t.Fatal(e)
	}
	defer source.DB.Close()
	if _, e = source.DB.Exec("UPDATE async_tasks SET stage=stage WHERE 1=0"); e == nil {
		t.Fatal("monitor source writable")
	}
}
func TestIntegrationOutboxRetryOrderAndClose(t *testing.T) {
	f := fixtureFor(t)
	task := f.seed(t, "failed", 0)
	f.observe(t, task, time.Now())
	event := f.events(t, task.ID)[0]
	calls := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		reply(w, 200, M{"code": 0})
	}))
	defer receiver.Close()
	cfg := f.cfg
	cfg.Sink = receiver.URL
	d := Dispatcher{cfg, f.db}
	if e := d.Deliver(f.ctx); e != nil {
		t.Fatal(e)
	}
	var row Outbox
	f.db.First(&row)
	if row.Status != "retry" {
		t.Fatal(row.Status)
	}
	f.db.Model(&Outbox{}).Where("id=?", row.ID).Update("next_retry_at", time.Now().Add(-time.Second))
	if e := d.Deliver(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.db.First(&row, "id=?", row.ID)
	if row.Status != "delivered" {
		t.Fatal(row.Status)
	}
	if _, e := CloseEvent(f.db, cfg, event.EventID, "测试关闭"); e != nil {
		t.Fatal(e)
	}
	if e := d.Deliver(f.ctx); e != nil {
		t.Fatal(e)
	}
	var all []Outbox
	f.db.Order("revision").Find(&all)
	if len(all) != 2 || all[1].Kind != "closed" || all[1].Status != "delivered" {
		t.Fatal(all)
	}
}
func TestIntegrationDiagnosisAndLateClose(t *testing.T) {
	for _, closeDuring := range []bool{false, true} {
		t.Run(fmt.Sprint(closeDuring), func(t *testing.T) {
			f := fixtureFor(t)
			task := f.seed(t, "failed", 0)
			f.observe(t, task, time.Now())
			event := f.events(t, task.ID)[0]
			d := Diagnosis{Cfg: f.cfg, DB: f.db}
			d.Collect = func(ctx context.Context, c Config, e Event, op string) M {
				if closeDuring {
					if _, err := CloseEvent(f.db, f.cfg, e.EventID, "停止实验"); err != nil {
						t.Fatal(err)
					}
				}
				return M(evidence.Result(evidence.Request{Event: e.EventID, Task: e.TaskID, Operation: op}, M{"records": []any{map[string]any{"kind": "model_error", "provider": "synthetic-provider", "error_code": "insufficient_quota"}}}, "", false))
			}
			if e := d.Prepare(f.ctx); e != nil {
				t.Fatal(e)
			}
			if e := d.RunOne(f.ctx); e != nil {
				t.Fatal(e)
			}
			if e := d.RunOne(f.ctx); e != nil {
				t.Fatal(e)
			}
			var job Job
			f.db.First(&job, "event_id=?", event.EventID)
			if job.ReportHash == nil {
				t.Fatal("no report")
			}
			var n int64
			f.db.Model(&Outbox{}).Where("kind='diagnosis'").Count(&n)
			if closeDuring {
				if job.Status != "canceled" || n != 0 {
					t.Fatal(job.Status, n)
				}
			} else if job.Status != "completed" || str(job.Report, "category") != "model_quota_exhausted" || n != 1 {
				t.Fatal(job.Status, job.Report, n)
			}
		})
	}
}
func TestIntegrationDeadlineOnRestart(t *testing.T) {
	f := fixtureFor(t)
	task := f.seed(t, "failed", 0)
	f.observe(t, task, time.Now())
	event := f.events(t, task.ID)[0]
	d := Diagnosis{Cfg: f.cfg, DB: f.db}
	if e := d.Prepare(f.ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.db.Model(&Job{}).Where("event_id=?", event.EventID).Updates(map[string]any{"status": "running", "claim_token": "old-process", "deadline_at": time.Now().Add(-time.Second)}).Error; e != nil {
		t.Fatal(e)
	}
	d.Collect = func(context.Context, Config, Event, string) M { t.Fatal("expired job reran"); return nil }
	if e := d.RunOne(f.ctx); e != nil {
		t.Fatal(e)
	}
	var job Job
	f.db.First(&job, "event_id=?", event.EventID)
	if job.Status != "timed_out" {
		t.Fatal(job.Status)
	}
}

func TestIntegrationModelFailureAndClose(t *testing.T) {
	for _, closeDuring := range []bool{false, true} {
		t.Run(fmt.Sprint(closeDuring), func(t *testing.T) {
			f := fixtureFor(t)
			task := f.seed(t, "failed", 0)
			f.observe(t, task, time.Now())
			event := f.events(t, task.ID)[0]
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if closeDuring {
					if _, err := CloseEvent(f.db, f.cfg, event.EventID, "实验关闭"); err != nil {
						t.Error(err)
					}
				}
				w.WriteHeader(429)
			}))
			defer srv.Close()
			f.cfg.Model = &ModelConfig{BaseURL: srv.URL, Model: "test", APIKey: "test-key"}
			d := Diagnosis{Cfg: f.cfg, DB: f.db, Collect: func(ctx context.Context, c Config, e Event, op string) M {
				return M(evidence.Result(evidence.Request{Event: e.EventID, Task: e.TaskID, Operation: op}, map[string]any{}, "", false))
			}}
			if err := d.Prepare(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := d.RunOne(f.ctx); err != nil {
				t.Fatal(err)
			}
			var job Job
			f.db.First(&job, "event_id=?", event.EventID)
			if str(job.Report, "diagnosis_mode") != "rule_based" || str(job.Report, "model_error") != "model_http_429" {
				t.Fatal("missing visible fallback")
			}
			var n int64
			f.db.Model(&Outbox{}).Where("kind='diagnosis'").Count(&n)
			if closeDuring {
				if job.Status != "canceled" || n != 0 {
					t.Fatal("late model notified after closure")
				}
			} else if job.Status != "completed" || n != 1 {
				t.Fatal("model failure blocked report")
			}
		})
	}
}
func TestIntegrationAPIAuthScopeAndSession(t *testing.T) {
	f := fixtureFor(t)
	task := f.seed(t, "failed", 0)
	f.observe(t, task, time.Now())
	event := f.events(t, task.ID)[0]
	server := httptest.NewServer((&Server{Cfg: f.cfg, DB: f.db}).Handler())
	defer server.Close()
	request := func(method, path string, payload any, auth bool, cookie *http.Cookie) *http.Response {
		raw, _ := json.Marshal(payload)
		r, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		if auth {
			r.Header.Set("Authorization", "Bearer "+f.cfg.Token)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	if request("GET", "/internal/events", nil, false, nil).StatusCode != 401 {
		t.Fatal("unprotected API")
	}
	if request("GET", "/internal/events", nil, true, nil).StatusCode != 200 {
		t.Fatal("auth failed")
	}
	if request("POST", "/internal/events/"+event.EventID+"/close", M{"reason": " "}, true, nil).StatusCode != 400 {
		t.Fatal("blank reason")
	}
	other := f.cfg
	other.Source = "unrelated"
	hidden := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/internal/events/"+event.EventID+"/report", nil)
	r.Header.Set("Authorization", "Bearer "+f.cfg.Token)
	(&Server{Cfg: other, DB: f.db}).Handler().ServeHTTP(hidden, r)
	if hidden.Code != 404 {
		t.Fatal("scope leak")
	}
	login := request("POST", "/auth/login", M{"token": f.cfg.Token}, false, nil)
	if login.StatusCode != 200 || len(login.Cookies()) == 0 {
		t.Fatal("login failed")
	}
	cookie := login.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	if request("POST", "/internal/events/"+event.EventID+"/close", M{"reason": "csrf"}, false, cookie).StatusCode != 401 {
		t.Fatal("CSRF accepted")
	}
}
func TestIntegrationConcurrentClose(t *testing.T) {
	f := fixtureFor(t)
	task := f.seed(t, "failed", 0)
	f.observe(t, task, time.Now())
	event := f.events(t, task.ID)[0]
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := CloseEvent(f.db, f.cfg, event.EventID, "concurrent close"); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int64
	f.db.Model(&Outbox{}).Where("kind='closed'").Count(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestIntegrationRealSSH(t *testing.T) {
	if os.Getenv("RUN_GO_SSH") != "1" {
		t.Skip("Go SSH target required")
	}
	f := fixtureFor(t)
	task := f.seed(t, "failed", 0)
	event := Event{EventID: uuid.NewString(), TaskID: task.ID}
	r := CollectSSH(f.ctx, f.cfg, event, "worker_status")
	if str(r, "unavailable_reason") != "" {
		t.Fatal(r)
	}
	if r["event_id"] != event.EventID {
		t.Fatal("bad binding")
	}
	cfg := f.cfg
	cfg.KnownHosts = "/tmp/nonexistent-known-hosts"
	if str(CollectSSH(f.ctx, cfg, event, "worker_status"), "unavailable_reason") == "" {
		t.Fatal("untrusted host accepted")
	}
}

var _ = errors.Is
