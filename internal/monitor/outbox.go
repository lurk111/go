package monitor

import (
	"bidmon/internal/checks"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

var HTTP = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type Dispatcher struct {
	Cfg Config
	DB  *gorm.DB
}

func (d *Dispatcher) Deliver(ctx context.Context) error {
	var row Outbox
	now := time.Now().UTC()
	err := d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		e := tx.Where("event_id IN (?) AND status IN ? AND next_retry_at<=?", eventScope(tx, d.Cfg), []string{"pending", "retry", "sending"}, now).Where("NOT EXISTS (SELECT 1 FROM delivery_outbox p WHERE p.event_id=delivery_outbox.event_id AND p.destination=delivery_outbox.destination AND p.revision<delivery_outbox.revision AND p.status IN ('pending','retry','sending'))").Order("next_retry_at").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&row).Error
		if e != nil {
			return e
		}
		if row.Attempts >= d.Cfg.Attempts {
			row.Status = "needs_attention"
			reason := "confirmation_unknown"
			row.Error = &reason
			return tx.Save(&row).Error
		}
		row.Attempts++
		row.Status = "sending"
		row.NextRetryAt = now.Add(30 * time.Second)
		return tx.Save(&row).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil || row.Status == "needs_attention" {
		return err
	}
	var event Event
	if err = d.DB.First(&event, "event_id=?", row.EventID).Error; err != nil {
		return err
	}
	if event.Status == "closed_unresolved" && row.Kind != "closed" {
		return d.DB.Model(&Outbox{}).Where("id=?", row.ID).Update("status", "canceled").Error
	}
	endpoint,token,confirm:=d.Cfg.Sink,d.Cfg.Token,"code_zero"
	payload:=M{};for k,v:=range row.Payload{if k!="destination_config"{payload[k]=v}}
	if channel:=obj(row.Payload,"destination_config");len(channel)>0{
		endpoint=str(channel,"url");confirm=str(channel,"confirm");token=""
		var ref checks.SecretRef;raw,_:=json.Marshal(channel["token_secret"]);_ = json.Unmarshal(raw,&ref)
		if ref.Env!=""||ref.File!=""{var err error;if d.Cfg.Runtime!=nil{token,err=d.Cfg.Runtime.resolveSecret(ref)}else{token,err=checks.ResolveSecret(ref)};if err!=nil{return d.deliveryFailure(ctx,row,"channel_secret_unavailable")}}
	}
	raw, _ := json.Marshal(payload)
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	if token!=""{req.Header.Set("Authorization", "Bearer "+token)}
	if key:=str(row.Payload,"idempotency_key");key!=""{req.Header.Set("Idempotency-Key",key)}
	response, e := HTTP.Do(req)
	ok := false
	retry := 0
	if e == nil {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 65537))
		response.Body.Close()
		var v struct {
			Code *int `json:"code"`
		}
		ok = readErr == nil && len(body) <= 65536 && response.StatusCode >= 200 && response.StatusCode < 300
		if confirm=="code_zero" {ok=ok&&json.Unmarshal(body,&v)==nil&&v.Code!=nil&&*v.Code==0}
		if n, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && n >= 0 {
			retry = min(300, n)
		}
	}
	return d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Outbox
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=?", row.ID).Error; e != nil {
			return e
		}
		if current.Status == "canceled" || current.Attempts != row.Attempts {
			return nil
		}
		if ok {
			now := time.Now().UTC()
			current.Status = "delivered"
			current.ConfirmedAt = &now
			current.Error = nil
		} else {
			current.Status = "retry"
			if current.Attempts >= d.Cfg.Attempts {
				current.Status = "needs_attention"
			}
			reason := "delivery_unconfirmed"
			current.Error = &reason
			if retry == 0 {
				retry = min(300, 2<<min(current.Attempts-1, 8))
			}
			current.NextRetryAt = time.Now().Add(time.Duration(float64(retry) * (1 + rand.Float64()*0.2) * float64(time.Second)))
		}
		return tx.Save(&current).Error
	})
}

func(d *Dispatcher)deliveryFailure(ctx context.Context,row Outbox,reason string)error{
	status:="retry";if row.Attempts>=d.Cfg.Attempts{status="needs_attention"}
	return d.DB.WithContext(ctx).Model(&Outbox{}).Where("id=? AND attempts=? AND status='sending'",row.ID,row.Attempts).Updates(map[string]any{"status":status,"error":reason,"next_retry_at":time.Now().UTC().Add(time.Duration(min(300,2<<min(row.Attempts-1,8)))*time.Second)}).Error
}
