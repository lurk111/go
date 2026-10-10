package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	_ "github.com/go-sql-driver/mysql"
	"strings"
	"time"
)

type Task struct {
	ID, User, Status, Stage string
	Created, Updated        time.Time
	Finished                *time.Time
}
type Source struct{ DB *sql.DB }

func OpenSource(dsn string) (*Source, error) {
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	return &Source{db}, nil
}
func (s *Source) Rows(ctx context.Context, where string, args ...any) ([]Task, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT task_id,user_id,status,COALESCE(stage,''),created_at,updated_at,finished_at FROM async_tasks WHERE biz_type='bidding' AND "+where, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if e = rows.Scan(&t.ID, &t.User, &t.Status, &t.Stage, &t.Created, &t.Updated, &t.Finished); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Source) Result(ctx context.Context, t Task) (M, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT status,CASE WHEN OCTET_LENGTH(result_json)<=1048576 THEN result_json ELSE NULL END FROM bidding_results WHERE task_id=? AND user_id=? ORDER BY id LIMIT 2", t.ID, t.User)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	n := 0
	status := ""
	var raw sql.NullString
	for rows.Next() {
		n++
		if e = rows.Scan(&status, &raw); e != nil {
			return nil, e
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if n == 0 {
		return M{"state": "missing"}, nil
	}
	if n > 1 {
		return M{"state": "ambiguous"}, nil
	}
	if status != "completed" || !raw.Valid {
		return M{"state": "invalid"}, nil
	}
	return Summarize(raw.String), nil
}

var lists = []string{"business_requirements.items", "technical_requirements.items", "qualification_review", "conformity_review", "waste_items", "score_requirements", "bid_evaluation_requirements", "bid_document_components"}
var instructionFields = strings.Fields("project_name project_number package_number subpackage_number section_number section_name package_name subpackage_name budget tenderee tender_agency project_domain project_summary for_sme bid_deadline bid_opening_time bid_opening_place bidding_method bid_validity_period consortium_allowed subcontract_allowed transfer_allowed bond_amount bond_account bond_payment_method bond_payment_remark blind_bid")

func Summarize(raw string) M {
	invalid := M{"state": "invalid"}
	var d M
	if json.Unmarshal([]byte(raw), &d) != nil {
		return invalid
	}
	counts := M{}
	total := 0
	for _, p := range lists {
		var v any = d
		for _, k := range strings.Split(p, ".") {
			switch m := v.(type) {
			case M:
				v = m[k]
			case map[string]any:
				v = m[k]
			default:
				return invalid
			}
		}
		a, ok := v.([]any)
		if !ok {
			return invalid
		}
		for _, x := range a {
			m, ok := x.(map[string]any)
			if !ok || len(m) == 0 {
				return invalid
			}
		}
		counts[p] = len(a)
		total += len(a)
	}
	ins, ok := d["bidder_instructions"].(map[string]any)
	if !ok {
		return invalid
	}
	for _, k := range instructionFields {
		if _, ok = ins[k]; !ok {
			return invalid
		}
	}
	filled := 0
	for _, x := range ins {
		m, ok := x.(map[string]any)
		if !ok {
			return invalid
		}
		v, ok := m["value"]
		if !ok {
			return invalid
		}
		switch z := v.(type) {
		case nil:
		case string:
			if strings.TrimSpace(z) != "" {
				filled++
			}
		case bool, float64:
			filled++
		default:
			return invalid
		}
	}
	counts["bidder_instructions"] = filled
	total += filled
	state := "present"
	if total == 0 {
		state = "empty"
	}
	return M{"state": state, "counts": counts}
}
