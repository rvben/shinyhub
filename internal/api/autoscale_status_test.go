package api

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestAutoscaleStatus_NotFound(t *testing.T) {
	s := buildAutoscaleStatus(db.AuditEvent{}, false, 5*time.Minute)
	if s.LastAction != "" {
		t.Fatalf("last_action = %v, want empty string", s.LastAction)
	}
	if s.InCooldown != false {
		t.Fatalf("in_cooldown = %v, want false", s.InCooldown)
	}
	if s.LastActionAt != nil {
		t.Fatalf("last_action_at = %v, want nil", s.LastActionAt)
	}
	if s.CooldownUntil != nil {
		t.Fatalf("cooldown_until = %v, want nil", s.CooldownUntil)
	}
}

func TestAutoscaleStatus_ScaleUp_InCooldown(t *testing.T) {
	// Event just happened (2 seconds ago); cooldown is 5 minutes -> in cooldown.
	now := time.Now()
	ev := db.AuditEvent{
		Action:    "autoscale_scale_up",
		CreatedAt: now.Add(-2 * time.Second),
	}
	s := buildAutoscaleStatus(ev, true, 5*time.Minute)

	if s.LastAction != "up" {
		t.Fatalf("last_action = %v, want up", s.LastAction)
	}
	if s.InCooldown != true {
		t.Fatalf("in_cooldown = %v, want true", s.InCooldown)
	}
	if s.CooldownUntil == nil {
		t.Fatalf("cooldown_until = nil, want a time value")
	}
	wantUntil := ev.CreatedAt.Add(5 * time.Minute)
	if !(*s.CooldownUntil).Equal(wantUntil) {
		t.Fatalf("cooldown_until = %v, want %v", *s.CooldownUntil, wantUntil)
	}
}

func TestAutoscaleStatus_ScaleDown_OutOfCooldown(t *testing.T) {
	// Event 10 minutes ago; cooldown is 5 minutes -> not in cooldown.
	ev := db.AuditEvent{
		Action:    "autoscale_scale_down",
		CreatedAt: time.Now().Add(-10 * time.Minute),
	}
	s := buildAutoscaleStatus(ev, true, 5*time.Minute)

	if s.LastAction != "down" {
		t.Fatalf("last_action = %v, want down", s.LastAction)
	}
	if s.InCooldown != false {
		t.Fatalf("in_cooldown = %v, want false (cooldown elapsed)", s.InCooldown)
	}
}
