package monitor

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Cfg    Config
	DB     *gorm.DB
	Assets fs.FS
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func body(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 8193))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("invalid_body")
	}
	return nil
}
func (s *Server) sign(v string) string {
	h := hmac.New(sha256.New, []byte(s.Cfg.Token))
	h.Write([]byte("session:" + v))
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Server) authorized(r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Cfg.Token)) == 1 {
		return true
	}
	cookie, e := r.Cookie("bid_mon_session")
	if e != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || !hmac.Equal([]byte(s.sign(parts[0])), []byte(parts[1])) {
		return false
	}
	expires, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || expires < time.Now().Unix() {
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		origin, e := url.Parse(r.Header.Get("Origin"))
		if e != nil || origin.Host != r.Host || r.Header.Get("X-Bid-Mon") != "1" {
			return false
		}
	}
	return true
}
func (s *Server) auth(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			reply(w, 401, M{"error": "请先登录"})
			return
		}
		fn(w, r)
	}
}
func (s *Server) exists(r *http.Request, id string) bool {
	var n int64
	return scoped(s.DB.WithContext(r.Context()).Model(&Event{}), s.Cfg).Where("event_id=?", id).Count(&n).Error == nil && n == 1
}
func (s *Server) Handler() http.Handler {
	mux := newAPIRoutes()
	mux.HandleFunc("GET /metrics",s.auth(s.metricsHandler().ServeHTTP))
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token string `json:"token"`
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host {
				reply(w, 403, M{"error": "来源无效"})
				return
			}
		}
		if body(r, &req) != nil || subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.Cfg.Token)) != 1 {
			reply(w, 401, M{"error": "登录凭据无效"})
			return
		}
		expiry := strconv.FormatInt(time.Now().Add(8*time.Hour).Unix(), 10)
		http.SetCookie(w, &http.Cookie{Name: "bid_mon_session", Value: expiry + "." + s.sign(expiry), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800, Secure: r.TLS != nil})
		reply(w, 200, M{"authenticated": true})
	})
	mux.HandleFunc("POST /auth/logout", s.auth(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "bid_mon_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		reply(w, 200, M{"ok": true})
	}))
	mux.HandleFunc("GET /auth/session", s.auth(func(w http.ResponseWriter, r *http.Request) { reply(w, 200, M{"authenticated": true}) }))
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, M{"status": "alive", "implementation": "go"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime!=nil {
			pool,err:=s.DB.DB();if err==nil{err=pool.PingContext(r.Context())}
			code:=200;if err!=nil{code=503}
			reply(w,code,M{"ready":err==nil,"implementation":"go","checks":s.Cfg.Runtime.Health()});return
		}
		var c Cursor
		e := s.DB.WithContext(r.Context()).First(&c, "key=?", s.Cfg.Source+"/"+s.Cfg.Environment).Error
		ok := e == nil && c.LastSuccessAt != nil && time.Since(*c.LastSuccessAt) < max(30*time.Second, 3*s.Cfg.Poll)
		code := 200
		if !ok {
			code = 503
		}
		reply(w, code, M{"ready": ok, "implementation": "go"})
	})
	mux.HandleFunc("GET /internal/events", s.auth(func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil {
			limit = max(1, min(500, n))
		}
		q := scoped(s.DB.WithContext(r.Context()), s.Cfg)
		for _, field := range []string{"status", "rule_code","check_id"} {
			if v := r.URL.Query().Get(field); v != "" {
				q = q.Where(field+"=?", v)
			}
		}
		if v := r.URL.Query().Get("task_id"); v != "" {
			q = q.Where("task_id LIKE ?", "%"+v+"%")
		}
		events := []Event{}
		if e := q.Order("opened_at DESC").Limit(limit).Find(&events).Error; e != nil {
			reply(w, 503, M{"error": "存储暂不可用"})
			return
		}
		reply(w, 200, events)
	}))
	mux.HandleFunc("GET /internal/events/{id}/report", s.auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !s.exists(r, id) {
			reply(w, 404, M{"error": "事件不存在"})
			return
		}
		var job Job
		err := s.DB.First(&job, "event_id=?", id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			reply(w, 200, M{"diagnosis_status": "not_requested", "report": nil, "evidence": []any{}})
			return
		}
		if err != nil {
			reply(w, 503, M{"error": "存储暂不可用"})
			return
		}
		reply(w, 200, job)
	}))
	mux.HandleFunc("GET /internal/events/{id}/audit", s.auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !s.exists(r, id) {
			reply(w, 404, M{"error": "事件不存在"})
			return
		}
		rows := []Audit{}
		if e := s.DB.Where("event_id=?", id).Order("at").Limit(200).Find(&rows).Error; e != nil {
			reply(w, 503, M{"error": "存储暂不可用"})
			return
		}
		reply(w, 200, rows)
	}))
	mux.HandleFunc("POST /internal/events/{id}/close", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason"`
		}
		if body(r, &req) != nil || strings.TrimSpace(req.Reason) == "" || len([]rune(req.Reason)) > 1000 {
			reply(w, 400, M{"error": "请填写关闭原因（1—1000 字）"})
			return
		}
		event, e := CloseEvent(s.DB.WithContext(r.Context()), s.Cfg, r.PathValue("id"), req.Reason)
		if e != nil {
			code := 503
			if errors.Is(e, gorm.ErrRecordNotFound) {
				code = 404
			}
			reply(w, code, M{"error": "无法关闭事件"})
			return
		}
		reply(w, 200, event)
	}))
	mux.HandleFunc("GET /internal/outbox", s.auth(func(w http.ResponseWriter, r *http.Request) {
		rows := []Outbox{}
		q := s.DB.Where("event_id IN (?)", eventScope(s.DB, s.Cfg))
		if id := r.URL.Query().Get("event_id"); id != "" {
			q = q.Where("event_id=?", id)
		}
		if e := q.Order("next_retry_at DESC").Limit(500).Find(&rows).Error; e != nil {
			reply(w, 503, M{"error": "存储暂不可用"})
			return
		}
		reply(w, 200, rows)
	}))
	mux.HandleFunc("GET /internal/metrics", s.auth(func(w http.ResponseWriter, r *http.Request) {
		result := M{"implementation": "go", "diagnosis_mode": "rule_based", "notification_channel": "local_sink"}
		for _, name := range []string{"events", "outbox", "diagnosis"} {
			table := map[string]string{"events": "monitor_events", "outbox": "delivery_outbox", "diagnosis": "diagnosis_jobs"}[name]
			var rows []struct {
				Status string
				Count  int
			}
			q := s.DB.Table(table).Select("status,COUNT(*) AS count").Where("event_id IN (?)", eventScope(s.DB, s.Cfg)).Group("status")
			if e := q.Scan(&rows).Error; e != nil {
				reply(w, 503, M{"error": "存储暂不可用"})
				return
			}
			counts := M{}
			for _, v := range rows {
				counts[v.Status] = v.Count
			}
			result[name] = counts
		}
		if s.Cfg.Runtime!=nil {
			_,version:=s.Cfg.Runtime.Current();health:=s.Cfg.Runtime.Health();result["config_version"]=version;result["checks"]=health;result["poll_seconds"]=s.Cfg.Poll.Seconds();result["model_enabled"]=s.Cfg.Model!=nil
			if s.Cfg.Model!=nil{result["model_name"]=s.Cfg.Model.Model}
			var latest *time.Time;failures:=0;for _,row:=range health{if row.LastSuccess!=nil&&(latest==nil||row.LastSuccess.After(*latest)){latest=row.LastSuccess};if row.Error!=""{failures++}}
			result["last_scan_at"]=latest;result["source_failures"]=failures;result["notification_channel"]="configured_channels"
			reply(w,200,result);return
		}
		var c Cursor
		if e := s.DB.First(&c, "key=?", s.Cfg.Source+"/"+s.Cfg.Environment).Error; e != nil {
			reply(w, 503, M{"error": "扫描尚未就绪"})
			return
		}
		result["last_scan_at"] = c.LastSuccessAt
		result["source_failures"] = c.Failures
		result["poll_seconds"] = s.Cfg.Poll.Seconds()
		result["model_enabled"] = s.Cfg.Model != nil
		if s.Cfg.Model != nil {
			result["diagnosis_mode"] = "model_assisted"
			result["model_name"] = s.Cfg.Model.Model
		}
		reply(w, 200, result)
	}))
	mux.HandleFunc("GET /internal/notifications", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime!=nil {
			rows:=[]Outbox{};err:=s.DB.WithContext(r.Context()).Where("status='delivered'").Order("confirmed_at DESC").Limit(500).Find(&rows).Error
			if err!=nil{reply(w,503,M{"error":"存储不可用"});return}
			payloads:=[]M{};for _,row:=range rows{p:=M{};for k,v:=range row.Payload{if k!="destination_config"{p[k]=v}};p["confirmed_at"]=row.ConfirmedAt;payloads=append(payloads,p)}
			reply(w,200,payloads);return
		}
		req, e := http.NewRequestWithContext(r.Context(), "GET", strings.TrimSuffix(s.Cfg.Sink, "/notifications")+"/notifications?limit=500", nil)
		if e != nil {
			reply(w, 502, M{"error": "通知服务不可用"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+s.Cfg.Token)
		res, e := HTTP.Do(req)
		if e != nil {
			reply(w, 502, M{"error": "通知服务不可用"})
			return
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		if e != nil || res.StatusCode != 200 {
			reply(w, 502, M{"error": "通知服务不可用"})
			return
		}
		var rows []M
		if json.Unmarshal(raw, &rows) != nil {
			reply(w, 502, M{"error": "通知响应无效"})
			return
		}
		ids := []string{}
		if e = s.DB.Model(&Event{}).Where("event_id IN (?)", eventScope(s.DB, s.Cfg)).Pluck("event_id", &ids).Error; e != nil {
			reply(w, 503, M{"error": "存储不可用"})
			return
		}
		valid := map[string]bool{}
		for _, id := range ids {
			valid[id] = true
		}
		filtered := []M{}
		for _, row := range rows {
			if valid[str(row, "event_id")] {
				filtered = append(filtered, row)
			}
		}
		reply(w, 200, filtered)
	}))
	s.generalRoutes(mux)
	if s.Assets != nil {
		mux.Handle("GET /", http.FileServerFS(s.Assets))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		mux.ServeHTTP(w, r)
	})
}
