package repository

import (
	"clientesFrecuentes/internal/model"
	"testing"
	"time"
)

func TestAppleEntitlementTransitions(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	s := model.BrandBillingState{Mode: "APPLE", ActiveBranches: 2}
	active := DeriveAppleState(s, "sku", "SELLOS", "SELLOS", 2, expiry, nil, nil, nil, nil, now)
	if active.Status != "ACTIVE" || appleWriteAllowed(active, 0) != nil || appleWriteAllowed(active, 1) != ErrAppleCapacity {
		t.Fatalf("capacity not enforced %+v", active)
	}
	grace := DeriveAppleState(s, "sku", "SELLOS", "SELLOS", 2, expiry, nil, nil, nil, nil, expiry.AddDate(0, 0, 6))
	if grace.Status != "GRACE" || appleWriteAllowed(grace, 0) != nil {
		t.Fatal("7-day grace missing")
	}
	expired := DeriveAppleState(s, "sku", "SELLOS", "SELLOS", 2, expiry, nil, nil, nil, nil, expiry.AddDate(0, 0, 7))
	if expired.Restriction != "EXPIRED" || appleWriteAllowed(expired, 0) != ErrAppleReadOnly {
		t.Fatal("expiry must restrict writes")
	}
	appleGrace := expiry.AddDate(0, 0, 3)
	extended := DeriveAppleState(s, "sku", "SELLOS", "SELLOS", 2, expiry, &appleGrace, nil, nil, nil, expiry.AddDate(0, 0, 8))
	if extended.Status != "GRACE" {
		t.Fatal("operational grace must follow Apple's coverage")
	}
	revoked := now.Add(-time.Second)
	r := DeriveAppleState(s, "sku", "SELLOS", "SELLOS", 2, expiry, &appleGrace, &revoked, nil, nil, now)
	if r.Status != "REVOKED" || appleWriteAllowed(r, 0) != ErrAppleReadOnly {
		t.Fatal("refund got grace")
	}
	r.ActiveBranches = 3
	if appleWriteAllowed(r, 0) != ErrAppleReadOnly {
		t.Fatal("capacity masked revoked status")
	}
	conflict := DeriveAppleState(s, "sku", "PUNTOS", "SELLOS", 2, expiry, nil, nil, nil, nil, now)
	if conflict.Status != "PROGRAM_CONFLICT" || appleWriteAllowed(conflict, 0) != ErrAppleProductConflict {
		t.Fatal("external program mismatch")
	}
	lower := DeriveAppleState(s, "sku", "SELLOS", "SELLOS", 1, expiry, nil, nil, nil, nil, now)
	if lower.Status != "OVER_CAPACITY" || appleWriteAllowed(lower, 0) != ErrAppleCapacity {
		t.Fatal("external downgrade bypass")
	}
	if appleWriteAllowed(model.BrandBillingState{Mode: "FREE"}, 100) != nil {
		t.Fatal("first free edition restricted")
	}
}
