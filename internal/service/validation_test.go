package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"

	"golang.org/x/crypto/bcrypt"
)

func TestRegisterCustomerRejectsWhenDemoSignupIsDisabled(t *testing.T) {
	svc := &Service{Config: config.Config{DemoSignupEnabled: false}}

	_, err := svc.RegisterCustomer(context.Background(), model.RegisterCustomerRequest{
		Email:    "customer@example.com",
		Password: "customer-pass",
		Name:     "Customer",
	})
	if !errors.Is(err, ErrDemoDisabled) {
		t.Fatalf("error = %v, want %v", err, ErrDemoDisabled)
	}
}

func TestValidPasswordHonorsBcryptByteLimit(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "minimum", value: "0123456789", valid: true},
		{name: "maximum bytes", value: string(make([]byte, 72)), valid: true},
		{name: "too long for bcrypt", value: string(make([]byte, 73)), valid: false},
		{name: "unicode byte overflow", value: "contraseña-segura-para-pruebas-🔐🔐🔐🔐🔐🔐🔐🔐🔐🔐🔐🔐", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validPassword(tt.value); got != tt.valid {
				t.Fatalf("validPassword byte_length=%d = %v, want %v", len(tt.value), got, tt.valid)
			}
		})
	}
}

func TestAnonymizeRequiresExactConfirmationAndRecentAuthentication(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	svc := &Service{Now: func() time.Time { return now }}
	if _, err := svc.AnonymizeCurrentUser(context.Background(), 1, now, 1, model.AnonymizeAccountRequest{Confirmation: "BORRAR"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("confirmation error=%v", err)
	}
	if _, err := svc.AnonymizeCurrentUser(context.Background(), 1, now.Add(-10*time.Minute-time.Second), 1, model.AnonymizeAccountRequest{Confirmation: "ANONIMIZAR"}); !errors.Is(err, ErrRecentAuthRequired) {
		t.Fatalf("recent auth error=%v", err)
	}
}

func TestUpdateCurrentUserRejectsEmptyAndInvalidPartialPatches(t *testing.T) {
	svc := &Service{}
	for name, patch := range map[string]model.UpdateAccountRequest{
		"empty":      {},
		"null name":  {Name: model.NullStringPatch()},
		"blank name": {Name: model.StringPatch("   ")},
		"bad photo":  {PhotoURL: model.StringPatch("javascript:alert(1)")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.UpdateCurrentUser(context.Background(), 1, 1, patch); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCreateInvitationRequiresUUIDIdempotencyKey(t *testing.T) {
	svc := &Service{}
	_, err := svc.CreateInvitation(context.Background(), 1, 2, "not-a-uuid", "request-id", model.CreateInvitationRequest{Email: "staff@example.com", Role: "OPERADOR", BranchIDs: []int64{3}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error=%v", err)
	}
}

func TestUpdateOperatorRequiresAtLeastOneAssignedBranch(t *testing.T) {
	svc := &Service{}
	role := "OPERADOR"
	empty := []int64{}
	_, err := svc.UpdateStaff(context.Background(), 1, 2, 3, 1, model.UpdateStaffRequest{Role: &role, BranchIDs: &empty})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateGoogleMerchantNormalizesAcceptedRegistration(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("demo-access-code"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	address := "  Calle 123  "
	svc := &Service{Config: config.Config{DemoAccessCodeHash: string(hash)}}
	got, err := svc.validateGoogleMerchant(&model.GoogleMerchantRegistration{
		BrandName: "  Mi Marca  ", BranchName: "  Principal  ", BranchAddress: &address,
		ProgramType: " puntos ", AccessCode: "demo-access-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.BrandName != "Mi Marca" || got.BranchName != "Principal" || got.BranchAddress == nil || *got.BranchAddress != "Calle 123" || got.ProgramType != "PUNTOS" || got.AccessCode != "" {
		t.Fatalf("normalized registration=%+v", got)
	}
}

func TestValidateGoogleMerchantRejectsMissingAndInvalidAccess(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("demo-access-code"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Config: config.Config{DemoAccessCodeHash: string(hash)}}
	if _, err = svc.validateGoogleMerchant(nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing registration error=%v", err)
	}
	if _, err = svc.validateGoogleMerchant(&model.GoogleMerchantRegistration{BrandName: "Marca", BranchName: "Principal", ProgramType: "SELLOS", AccessCode: "wrong-access-code"}); !errors.Is(err, ErrDemoAccess) {
		t.Fatalf("invalid access error=%v", err)
	}
}
