package monitor

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"strings"
	"sync"
)

// Keep domain handlers independent of Gin while centralizing API routing and
// recovery. PathValue preserves the existing public HTTP contract.
type apiRoutes struct{ engine *gin.Engine }

var ginModeOnce sync.Once

func newAPIRoutes() *apiRoutes {
	ginModeOnce.Do(func() { gin.SetMode(gin.ReleaseMode) })
	g := gin.New()
	g.Use(gin.Recovery())
	return &apiRoutes{g}
}
func (m *apiRoutes) HandleFunc(pattern string, handler http.HandlerFunc) {
	parts := strings.SplitN(pattern, " ", 2)
	path := strings.ReplaceAll(strings.ReplaceAll(parts[1], "{", ":"), "}", "")
	m.engine.Handle(parts[0], path, func(c *gin.Context) {
		for _, p := range c.Params {
			c.Request.SetPathValue(p.Key, p.Value)
		}
		handler(c.Writer, c.Request)
	})
}
func (m *apiRoutes) Handle(pattern string, handler http.Handler) {
	if pattern == "GET /" {
		m.engine.NoRoute(func(c *gin.Context) {
			if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
				handler.ServeHTTP(c.Writer, c.Request)
			} else {
				reply(c.Writer, 404, M{"error": "接口不存在"})
			}
		})
		return
	}
	m.HandleFunc(pattern, handler.ServeHTTP)
}
func (m *apiRoutes) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.engine.ServeHTTP(w, r) }

func (s *Server) metricsHandler() http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "bid_mon_configured_checks", Help: "Number of configured checks."}, func() float64 {
		if s.Cfg.Runtime == nil {
			return 0
		}
		return float64(len(s.Cfg.Runtime.Health()))
	}))
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "bid_mon_failed_checks", Help: "Checks whose latest execution failed."}, func() float64 {
		if s.Cfg.Runtime == nil {
			return 0
		}
		n := 0
		for _, h := range s.Cfg.Runtime.Health() {
			if h.Error != "" {
				n++
			}
		}
		return float64(n)
	}))
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "bid_mon_last_check_duration_seconds", Help: "Sum of latest check execution durations."}, func() float64 {
		if s.Cfg.Runtime == nil {
			return 0
		}
		var n int64
		for _, h := range s.Cfg.Runtime.Health() {
			n += h.DurationMS
		}
		return float64(n) / 1000
	}))
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
