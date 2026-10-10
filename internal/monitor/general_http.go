package monitor

import (
	"bidmon/internal/checks"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"net/http"
	"time"
)

func configRequest(r *http.Request) (*checks.Document, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	return checks.Parse(raw)
}
func (s *Server) generalRoutes(mux *apiRoutes) {
	mux.HandleFunc("POST /internal/retention/preview", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var policy checks.RetentionSpec
		if body(r, &policy) != nil {
			reply(w, 400, M{"error": "保留预览参数无效"})
			return
		}
		result, err := PreviewRetention(r.Context(), s.DB, policy, time.Now().UTC())
		if err != nil {
			reply(w, 400, M{"error": "预览失败，请检查天数及存储状态"})
			return
		}
		reply(w, 200, result)
	}))
	mux.HandleFunc("POST /internal/sources/{id}/test", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 409, M{"error": "二期配置管理未启用"})
			return
		}
		doc, err := configRequest(r)
		if err != nil {
			reply(w, 400, M{"error": Redact(err.Error())})
			return
		}
		for _, spec := range doc.Sources {
			if spec.ID == r.PathValue("id") {
				runner, err := checks.NewRunner(spec, s.Cfg.Runtime.resolveSecret)
				if err != nil {
					reply(w, 400, M{"error": "连接配置或凭据不可用"})
					return
				}
				defer runner.Close()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				if runner.Ping(ctx) != nil {
					reply(w, 400, M{"error": "数据库连接失败，请核对容器可访问的地址、账号与授权"})
					return
				}
				reply(w, 200, M{"connected": true, "source": spec.ID, "note": "连接通过；查询权限和字段需进一步试运行"})
				return
			}
		}
		reply(w, 404, M{"error": "来源不存在"})
	}))
	mux.HandleFunc("POST /internal/checks/{id}/evidence-preview", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 409, M{"error": "二期配置管理未启用"})
			return
		}
		var input struct {
			EntityKey string `json:"entity_key"`
		}
		if body(r, &input) != nil || input.EntityKey == "" || len(input.EntityKey) > 512 {
			reply(w, 400, M{"error": "业务对象 ID 无效"})
			return
		}
		doc, _ := s.Cfg.Runtime.Current()
		for _, spec := range doc.Checks {
			if spec.ID == r.PathValue("id") {
				raw, _ := json.Marshal(spec)
				var definition M
				_ = json.Unmarshal(raw, &definition)
				event := Event{EventID: uuid.NewString(), CheckID: spec.ID, EntityKey: input.EntityKey, TaskID: input.EntityKey, Definition: M{"check": definition, "evidence_config_file": doc.EvidenceConfigFile}}
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				reply(w, 200, M{"dry_run": true, "evidence": collectGeneric(ctx, s.Cfg, event, "logs")})
				return
			}
		}
		reply(w, 404, M{"error": "检查不存在"})
	}))
	mux.HandleFunc("POST /internal/channels/{id}/test", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 409, M{"error": "二期配置管理未启用"})
			return
		}
		doc, err := configRequest(r)
		if err != nil {
			reply(w, 400, M{"error": Redact(err.Error())})
			return
		}
		for _, ch := range doc.Channels {
			if ch.ID == r.PathValue("id") {
				ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
				defer cancel()
				if err := s.testChannel(ctx, ch); err != nil {
					reply(w, 400, M{"error": err.Error()})
					return
				}
				reply(w, 200, M{"confirmed": true, "test_message": true, "channel": ch.ID})
				return
			}
		}
		reply(w, 404, M{"error": "渠道不存在"})
	}))
	mux.HandleFunc("PUT /internal/secrets/{id}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 409, M{"error": "二期配置管理未启用"})
			return
		}
		var input struct {
			Value string `json:"value"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 65537))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			reply(w, 400, M{"error": "凭据格式无效"})
			return
		}
		if err := s.Cfg.Runtime.SaveSecret(r.PathValue("id"), input.Value); err != nil {
			reply(w, 400, M{"error": err.Error()})
			return
		}
		reply(w, 200, M{"saved": true, "reference": checks.SecretRef{File: "stored:" + r.PathValue("id")}})
	}))
	mux.HandleFunc("GET /internal/secrets", s.auth(func(w http.ResponseWriter, r *http.Request) {
		rows := []StoredSecret{}
		if err := s.DB.WithContext(r.Context()).Order("id").Find(&rows).Error; err != nil {
			reply(w, 503, M{"error": "存储不可用"})
			return
		}
		reply(w, 200, rows)
	}))
	mux.HandleFunc("GET /internal/config", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 200, M{"enabled": false})
			return
		}
		doc, version := s.Cfg.Runtime.Current()
		reply(w, 200, M{"enabled": true, "document": doc, "version": version, "checks": s.Cfg.Runtime.Health()})
	}))
	mux.HandleFunc("POST /internal/config/validate", s.auth(func(w http.ResponseWriter, r *http.Request) {
		doc, err := configRequest(r)
		if err != nil {
			reply(w, 400, M{"error": Redact(err.Error())})
			return
		}
		reply(w, 200, M{"valid": true, "document": doc})
	}))
	mux.HandleFunc("PUT /internal/config", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 409, M{"error": "请使用二期启动入口启用配置管理"})
			return
		}
		doc, err := configRequest(r)
		if err != nil {
			reply(w, 400, M{"error": Redact(err.Error())})
			return
		}
		if doc.MonitorPresales != s.Cfg.Document.MonitorPresales {
			reply(w, 400, M{"error": "原业务监测模式需通过启动配置修改并重启"})
			return
		}
		if err = s.Cfg.Runtime.Install(r.Context(), doc, "administrator"); err != nil {
			reply(w, 503, M{"error": "配置保存失败"})
			return
		}
		_, version := s.Cfg.Runtime.Current()
		reply(w, 200, M{"saved": true, "version": version})
	}))
	mux.HandleFunc("POST /internal/checks/{id}/preview", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 409, M{"error": "二期配置管理未启用"})
			return
		}
		doc, err := configRequest(r)
		if err != nil {
			reply(w, 400, M{"error": Redact(err.Error())})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		rows, err := s.Cfg.Runtime.Preview(ctx, doc, r.PathValue("id"))
		if err != nil {
			reply(w, 400, M{"error": err.Error()})
			return
		}
		decisions := []M{}
		for _, spec := range doc.Checks {
			if spec.ID == r.PathValue("id") {
				compiled, _ := checks.CompileRules(spec)
				for _, obs := range rows {
					results := []checks.RuleResult{}
					for _, rule := range compiled {
						results = append(results, rule.Evaluate(obs, nil, time.Now().UTC()))
					}
					decisions = append(decisions, M{"entity_key": obs.EntityKey, "rules": results})
				}
			}
		}
		for i := range rows {
			rows[i].Fields = map[string]any(evidenceFields(rows[i].Fields))
		}
		reply(w, 200, M{"dry_run": true, "observations": rows, "decisions": decisions, "note": "试运行无历史 previous 数据，不累计确认次数"})
	}))
	mux.HandleFunc("GET /internal/config/history", s.auth(func(w http.ResponseWriter, r *http.Request) {
		rows := []ConfigVersion{}
		if err := s.DB.WithContext(r.Context()).Order("created_at DESC").Limit(20).Find(&rows).Error; err != nil {
			reply(w, 503, M{"error": "存储不可用"})
			return
		}
		reply(w, 200, rows)
	}))
	mux.HandleFunc("POST /internal/config/rollback/{version}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.Runtime == nil {
			reply(w, 409, M{"error": "二期配置管理未启用"})
			return
		}
		var row ConfigVersion
		if s.DB.WithContext(r.Context()).First(&row, "version=?", r.PathValue("version")).Error != nil {
			reply(w, 404, M{"error": "配置版本不存在"})
			return
		}
		raw, _ := json.Marshal(row.Document)
		doc, err := checks.Parse(raw)
		if err != nil {
			reply(w, 400, M{"error": "历史配置无效"})
			return
		}
		if doc.MonitorPresales != s.Cfg.Document.MonitorPresales {
			reply(w, 400, M{"error": "历史模式与运行实例不兼容"})
			return
		}
		if err = s.Cfg.Runtime.Install(r.Context(), doc, "administrator"); err != nil {
			reply(w, 503, M{"error": "配置回退失败"})
			return
		}
		_, version := s.Cfg.Runtime.Current()
		reply(w, 200, M{"saved": true, "version": version})
	}))
}
