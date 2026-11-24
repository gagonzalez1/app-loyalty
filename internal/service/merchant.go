package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/web"

	"clientesFrecuentes/internal/password"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func (s *Service) RegisterDemoMerchant(ctx context.Context, key, requestID string, req model.RegisterDemoMerchantRequest) (repository.IdempotentResult, error) {
	if !s.Config.DemoSignupEnabled {
		return repository.IdempotentResult{}, ErrDemoDisabled
	}
	if _, err := uuid.Parse(key); err != nil {
		return repository.IdempotentResult{}, ErrInvalidRequest
	}
	email, err := normalizeEmail(req.Email)
	if err != nil || !validPassword(req.Password) {
		return repository.IdempotentResult{}, ErrInvalidRequest
	}
	owner, ownerLastName, err := registrationNames(req.OwnerName, req.OwnerLastName)
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	brand, err := cleanName(req.BrandName, 120)
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	branch, err := cleanName(req.BranchName, 120)
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	location := model.BranchRegistrationLocation{
		BranchAddress: req.BranchAddress, BranchLocality: req.BranchLocality, BranchProvince: req.BranchProvince,
		BranchPostalCode: req.BranchPostalCode, BranchLatitude: req.BranchLatitude, BranchLongitude: req.BranchLongitude, PublicProfile: req.PublicProfile,
	}
	if err := cleanRegistrationBranchLocation(&location); err != nil {
		return repository.IdempotentResult{}, err
	}
	programType, err := normalizeProgramType(req.ProgramType)
	if err != nil {
		return repository.IdempotentResult{}, ErrInvalidRequest
	}
	if err = s.checkCaptcha(ctx, "signup"); err != nil {
		return repository.IdempotentResult{}, err
	}
	passwordHash, err := password.Generate(ctx, []byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	// This fingerprint is persisted. Key it so a database leak cannot turn the
	// low-entropy password field into a fast, offline SHA-256 oracle.
	fingerprint := KeyedFingerprint(s.Config.QRPepper, struct {
		Email, Password, OwnerName, OwnerLastName, BrandName, BranchName string
		BranchAddress                                                    *string
		BranchLocality                                                   *string  `json:",omitempty"`
		BranchProvince                                                   *string  `json:",omitempty"`
		BranchPostalCode                                                 *string  `json:",omitempty"`
		BranchLatitude                                                   *float64 `json:",omitempty"`
		BranchLongitude                                                  *float64 `json:",omitempty"`
		ProgramType, ReferralCode                                        string
		PublicProfile                                                    *model.BranchPublicProfile `json:",omitempty"`
	}{email, req.Password, owner, ownerLastName, brand, branch, location.BranchAddress, location.BranchLocality, location.BranchProvince, location.BranchPostalCode, location.BranchLatitude, location.BranchLongitude, programType, strings.ToUpper(strings.TrimSpace(req.ReferralCode)), location.PublicProfile})
	if req.VerificationMethod != "" {
		fingerprint = KeyedFingerprint(s.Config.QRPepper, struct {
			Registration       []byte
			VerificationMethod string
		}{fingerprint, req.VerificationMethod})
	}

	credentials, err := newSessionCredentials()
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	var verifiedAt *time.Time
	registrationToken, verificationHash, verificationExpires, verificationMessage, err := s.registrationVerification(email, req.VerificationMethod, key)
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	if !s.Config.EmailVerificationRequired {
		now := s.Now()
		verifiedAt = &now
	}
	var result repository.IdempotentResult
	err = retryRegistration(ctx, func() error {
		var e error
		result, e = s.Repo.CreateDemoMerchant(ctx, key, fingerprint, email, string(passwordHash), owner, ownerLastName, brand, branch, location, programType, req.ReferralCode, credentials.id, credentials.hash, credentials.expiresAt, credentials.authTime, verifiedAt, verificationHash, verificationExpires, verificationMessage, func(u model.User, m model.MerchantContext) ([]byte, error) {
			if s.Config.EmailVerificationRequired {
				return json.Marshal(web.Envelope[model.DemoMerchantData]{Data: model.DemoMerchantData{User: u, Merchant: m, VerificationRequired: true, VerificationMethod: req.VerificationMethod}, RequestID: requestID})
			}
			merchantSession, e := s.session(u, credentials)
			if e != nil {
				return nil, e
			}
			// Idempotency persistence must never retain the bearer-equivalent
			// refresh secret. It is injected only into the in-memory response.
			merchantSession.RefreshToken = ""
			return json.Marshal(web.Envelope[model.DemoMerchantData]{Data: model.DemoMerchantData{Session: &merchantSession, User: u, Merchant: m}, RequestID: requestID})
		})
		return e
	})
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	var envelope web.Envelope[model.DemoMerchantData]
	if err = json.Unmarshal(result.Body, &envelope); err != nil {
		return repository.IdempotentResult{}, err
	}
	if envelope.Data.VerificationRequired {
		if envelope.Data.VerificationMethod == "code" {
			envelope.Data.RegistrationToken = registrationToken
		}
		result.Body, err = json.Marshal(envelope)
		return result, err
	}
	if result.Replayed {
		sess, sessionErr := s.issueSession(ctx, envelope.Data.User)
		err = sessionErr
		envelope.Data.Session = &sess
		if err != nil {
			return repository.IdempotentResult{}, err
		}
	} else {
		envelope.Data.Session.RefreshToken = credentials.raw
	}
	result.Body, err = json.Marshal(envelope)
	if err != nil {
		return repository.IdempotentResult{}, err
	}
	return result, nil
}

func cleanRegistrationBranchLocation(location *model.BranchRegistrationLocation) error {
	fields := []struct {
		value **string
		max   int
	}{
		{&location.BranchAddress, 300},
		{&location.BranchLocality, 120},
		{&location.BranchProvince, 120},
		{&location.BranchPostalCode, 20},
	}
	for _, field := range fields {
		if *field.value == nil {
			continue
		}
		trimmed := strings.TrimSpace(**field.value)
		if len([]rune(trimmed)) > field.max {
			return ErrInvalidRequest
		}
		if trimmed == "" {
			*field.value = nil
		} else {
			*field.value = &trimmed
		}
	}
	if (location.BranchLatitude == nil) != (location.BranchLongitude == nil) {
		return ErrInvalidRequest
	}
	if location.BranchLatitude != nil && (*location.BranchLatitude < -90 || *location.BranchLatitude > 90 || *location.BranchLongitude < -180 || *location.BranchLongitude > 180) {
		return ErrInvalidRequest
	}
	return cleanBranchPublicProfile(location.PublicProfile)
}

func normalizeProgramType(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value != "SELLOS" && value != "PUNTOS" {
		return "", ErrInvalidRequest
	}
	return value, nil
}

func (s *Service) ListBrands(ctx context.Context, actorID int64) ([]model.MerchantContext, error) {
	return s.Repo.ListMerchantContexts(ctx, actorID)
}

func (s *Service) Brand(ctx context.Context, actorID, brandID int64) (model.MerchantContext, error) {
	return s.Repo.GetMerchantContext(ctx, actorID, brandID)
}

func (s *Service) Benefits(ctx context.Context, actorID, brandID int64) ([]model.Benefit, error) {
	return s.Repo.ListBenefits(ctx, actorID, brandID)
}

func (s *Service) CreateBenefit(ctx context.Context, actorID, brandID int64, req model.CreateBenefitRequest) (model.Benefit, error) {
	if req.CanonicalName != "" {
		req.Name = req.CanonicalName
	}
	if req.CanonicalRequirement != 0 {
		req.Requirement = req.CanonicalRequirement
	}
	if req.CanonicalExpiresOn != nil {
		req.ExpiresOn = req.CanonicalExpiresOn
	}
	name, err := cleanName(req.Name, 120)
	if err != nil || req.Requirement < 1 || req.Requirement > 10000000 || len(req.Description) > 1000 {
		return model.Benefit{}, ErrInvalidRequest
	}
	expiresOn, err := parseOptionalBenefitDate(req.ExpiresOn)
	if err != nil {
		return model.Benefit{}, err
	}
	return s.Repo.CreateBenefit(ctx, actorID, brandID, name, req.Description, req.Requirement, expiresOn)
}

func (s *Service) BrandMovements(ctx context.Context, actorID, brandID int64, page, size int, search, date string) ([]model.Movement, web.Pagination, error) {
	search = strings.TrimSpace(search)
	date = strings.TrimSpace(date)
	if len([]rune(search)) > 120 {
		return nil, web.Pagination{}, ErrInvalidRequest
	}
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return nil, web.Pagination{}, ErrInvalidRequest
		}
	}
	items, total, err := s.Repo.ListBrandMovements(ctx, actorID, brandID, page, size, search, date)
	return items, pagination(page, size, total), err
}

func (s *Service) ReverseBrandMovement(ctx context.Context, actorID, brandID int64, operationID string) (model.MovementReversal, error) {
	if _, err := uuid.Parse(operationID); err != nil {
		return model.MovementReversal{}, ErrInvalidRequest
	}
	var result model.MovementReversal
	err := retry(ctx, func() error {
		var reverseErr error
		result, reverseErr = s.Repo.ReverseBrandMovement(ctx, actorID, brandID, operationID)
		return reverseErr
	})
	return result, err
}

func (s *Service) BrandCustomers(ctx context.Context, actorID, brandID int64, page, size int, search string) ([]model.BrandCustomer, web.Pagination, error) {
	search = strings.TrimSpace(search)
	if len([]rune(search)) > 120 {
		return nil, web.Pagination{}, ErrInvalidRequest
	}
	items, total, err := s.Repo.ListBrandCustomers(ctx, actorID, brandID, page, size, search)
	return items, pagination(page, size, total), err
}

func (s *Service) BrandCustomerLookup(ctx context.Context, actorID, brandID int64, customerCode string) (model.BrandCustomer, error) {
	customerID, err := s.resolveCustomerCodeID(ctx, customerCode)
	if err != nil {
		return model.BrandCustomer{}, err
	}
	return s.Repo.LookupBrandCustomer(ctx, actorID, brandID, customerID)
}

func (s *Service) BrandMetrics(ctx context.Context, actorID, brandID int64) (model.BrandMetricsSummary, error) {
	return s.Repo.BrandMetricsSummary(ctx, actorID, brandID)
}
