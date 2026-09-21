package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
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

func TestAnonymizeRequiresExactConfirmation(t *testing.T) {
	svc := &Service{}
	if _, err := svc.AnonymizeCurrentUser(context.Background(), 1, 1, model.AnonymizeAccountRequest{Confirmation: "BORRAR"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("confirmation error=%v", err)
	}
}

func TestUpdateCurrentUserRejectsEmptyAndInvalidPartialPatches(t *testing.T) {
	svc := &Service{}
	for name, patch := range map[string]model.UpdateAccountRequest{
		"empty":      {},
		"null name":  {Name: model.NullStringPatch()},
		"blank name": {Name: model.StringPatch("   ")},
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

func TestRegisterInvitationValidatesTokenAndCredentialsBeforePersistence(t *testing.T) {
	svc := &Service{}
	tests := []struct {
		name  string
		token string
		req   model.RegisterInvitationRequest
	}{
		{name: "invalid token", token: "short", req: model.RegisterInvitationRequest{Name: "Operador", Password: "operator-pass"}},
		{name: "blank name", token: strings.Repeat("a", 40), req: model.RegisterInvitationRequest{Name: " ", Password: "operator-pass"}},
		{name: "short password", token: strings.Repeat("a", 40), req: model.RegisterInvitationRequest{Name: "Operador", Password: "short"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.RegisterInvitation(context.Background(), tt.token, tt.req); !errors.Is(err, ErrInvalidRequest) && !errors.Is(err, ErrIdentityToken) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestValidateGoogleMerchantNormalizesAcceptedRegistration(t *testing.T) {
	address, locality, province, postalCode := "  Calle 123  ", "  Rosario  ", "  Santa Fe  ", "  S2000  "
	latitude, longitude := -32.94682, -60.63932
	svc := &Service{}
	got, err := svc.validateGoogleMerchant(&model.GoogleMerchantRegistration{
		BrandName: "  Mi Marca  ", BranchName: "  Principal  ", BranchAddress: &address, BranchLocality: &locality,
		BranchProvince: &province, BranchPostalCode: &postalCode, BranchLatitude: &latitude, BranchLongitude: &longitude,
		ProgramType: " puntos ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.BrandName != "Mi Marca" || got.BranchName != "Principal" || got.BranchAddress == nil || *got.BranchAddress != "Calle 123" || got.BranchLocality == nil || *got.BranchLocality != "Rosario" || got.BranchProvince == nil || *got.BranchProvince != "Santa Fe" || got.BranchPostalCode == nil || *got.BranchPostalCode != "S2000" || got.BranchLatitude == nil || *got.BranchLatitude != latitude || got.BranchLongitude == nil || *got.BranchLongitude != longitude || got.ProgramType != "PUNTOS" {
		t.Fatalf("normalized registration=%+v", got)
	}
}

func TestCleanRegistrationBranchLocationRequiresCoordinatePairAndRanges(t *testing.T) {
	latitude, longitude := -34.6, -58.4
	for name, location := range map[string]model.BranchRegistrationLocation{
		"missing longitude": {BranchLatitude: &latitude},
		"missing latitude":  {BranchLongitude: &longitude},
		"invalid latitude":  {BranchLatitude: float64Pointer(91), BranchLongitude: &longitude},
		"invalid longitude": {BranchLatitude: &latitude, BranchLongitude: float64Pointer(-181)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := cleanRegistrationBranchLocation(&location); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func float64Pointer(value float64) *float64 { return &value }

func TestValidateGoogleMerchantRejectsMissingRegistration(t *testing.T) {
	svc := &Service{}
	if _, err := svc.validateGoogleMerchant(nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing registration error=%v", err)
	}
}

func TestProfilePhotoObjectKeyOnlyAcceptsManagedProfileReferences(t *testing.T) {
	key, ok := profilePhotoObjectKey("s3://puntazo/profiles/7/photo.png")
	if !ok || key != "profiles/7/photo.png" {
		t.Fatalf("key=%q ok=%t", key, ok)
	}
	for _, reference := range []string{"https://example.com/photo.png", "s3://puntazo/brands/7/logo.png", "s3://puntazo/profiles/../secret"} {
		if _, accepted := profilePhotoObjectKey(reference); accepted {
			t.Fatalf("accepted unsafe reference %q", reference)
		}
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
