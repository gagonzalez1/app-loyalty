package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/identitycode"
	"clientesFrecuentes/internal/mailer"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"

	"clientesFrecuentes/internal/password"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const refreshLifetime = 30 * 24 * time.Hour

type sessionCredentials struct {
	id        string
	raw       string
	hash      []byte
	expiresAt time.Time
	authTime  time.Time
}

func (s *Service) RegisterCustomer(ctx context.Context, req model.RegisterCustomerRequest) (model.RegisterCustomerData, error) {
	if !s.Config.DemoSignupEnabled {
		return model.RegisterCustomerData{}, ErrDemoDisabled
	}
	email, err := normalizeEmail(req.Email)
	if err != nil || !validPassword(req.Password) {
		return model.RegisterCustomerData{}, ErrInvalidRequest
	}
	name, lastName, err := registrationNames(req.Name, req.LastName)
	if err != nil {
		return model.RegisterCustomerData{}, err
	}
	if err = s.checkCaptcha(ctx, "signup"); err != nil {
		return model.RegisterCustomerData{}, err
	}
	hash, err := password.Generate(ctx, []byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return model.RegisterCustomerData{}, err
	}
	provisional := make([]byte, 32)
	if _, err = rand.Read(provisional); err != nil {
		return model.RegisterCustomerData{}, err
	}
	var verifiedAt *time.Time
	registrationToken, verificationHash, verificationExpires, message, err := s.registrationVerification(email, req.VerificationMethod, "")
	if err != nil {
		return model.RegisterCustomerData{}, err
	}
	if !s.Config.EmailVerificationRequired {
		now := s.Now()
		verifiedAt = &now
	}
	// The final QR is derived after PostgreSQL assigns the immutable user id.
	u, err := s.Repo.CreateCustomer(ctx, email, string(hash), name, lastName, provisional, func(id int64) []byte { _, finalHash := s.QRForUser(id); return finalHash }, verifiedAt, verificationHash, verificationExpires, message)
	if err != nil {
		return model.RegisterCustomerData{}, err
	}
	result := model.RegisterCustomerData{User: u, VerificationRequired: s.Config.EmailVerificationRequired}
	if s.Config.EmailVerificationRequired {
		if req.VerificationMethod == "code" {
			result.VerificationMethod = "code"
			result.RegistrationToken = registrationToken
		}
		return result, nil
	}
	session, err := s.issueSession(ctx, u)
	if err != nil {
		return model.RegisterCustomerData{}, err
	}
	result.Session = &session
	return result, nil
}

func (s *Service) Login(ctx context.Context, req model.LoginRequest) (model.AuthData, error) {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return model.AuthData{}, ErrInvalidCredentials
	}
	u, err := s.Repo.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.AuthData{}, ErrInvalidCredentials
		}
		return model.AuthData{}, err
	}
	if !u.User.Active || u.PasswordHash == nil || len(req.Password) > 72 {
		return model.AuthData{}, ErrInvalidCredentials
	}
	if err = password.Compare(ctx, []byte(*u.PasswordHash), []byte(req.Password)); err != nil {
		if errors.Is(err, password.ErrBusy) {
			return model.AuthData{}, err
		}
		return model.AuthData{}, ErrInvalidCredentials
	}
	if u.EmailVerifiedAt == nil {
		return model.AuthData{}, ErrEmailUnverified
	}
	session, err := s.issueSession(ctx, u.User)
	if err != nil {
		return model.AuthData{}, err
	}
	return model.AuthData{Session: session, User: u.User}, nil
}

func (s *Service) LoginGoogle(ctx context.Context, req model.GoogleAuthRequest) (model.AuthData, error) {
	if strings.TrimSpace(req.IDToken) == "" {
		return model.AuthData{}, ErrInvalidRequest
	}
	verify := s.VerifyGoogleToken
	if verify == nil {
		verify = auth.VerifyGoogleIdentity
	}
	identity, err := verify(ctx, req.IDToken)
	googleID, email := identity.GoogleID, identity.Email
	name, lastName := identity.Name, identity.LastName
	if identitycode.Initial(name) == "" {
		name = req.Name
	}
	if identitycode.Initial(lastName) == "" {
		lastName = req.LastName
	}
	if err != nil {
		return model.AuthData{}, ErrInvalidCredentials
	}
	email, err = normalizeEmail(email)
	if err != nil {
		return model.AuthData{}, ErrInvalidCredentials
	}
	u, err := s.Repo.ResolveGoogleUser(ctx, googleID, email)
	if err == nil {
		return s.googleSession(ctx, u)
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return model.AuthData{}, err
	}
	if req.AccountType == nil || strings.TrimSpace(*req.AccountType) == "" {
		return model.AuthData{}, &SignupProfileError{Name: name, LastName: lastName, AccountTypeRequired: true}
	}
	accountType := strings.TrimSpace(*req.AccountType)
	if accountType != "CLIENTE_FINAL" && accountType != "PERSONAL_MARCA" {
		return model.AuthData{}, ErrInvalidRequest
	}
	if !s.Config.DemoSignupEnabled {
		return model.AuthData{}, ErrDemoDisabled
	}
	name, lastName, err = registrationNames(name, lastName)
	if err != nil {
		return model.AuthData{}, &SignupProfileError{Name: identity.Name, LastName: identity.LastName}
	}
	if err = s.checkCaptcha(ctx, "signup"); err != nil {
		return model.AuthData{}, err
	}
	if accountType == "CLIENTE_FINAL" {
		if req.MerchantRegistration != nil {
			return model.AuthData{}, ErrInvalidRequest
		}
		provisional := make([]byte, 32)
		if _, err = rand.Read(provisional); err != nil {
			return model.AuthData{}, err
		}
		err = retryRegistration(ctx, func() error {
			u, err = s.Repo.CreateGoogleCustomer(ctx, googleID, email, name, lastName, provisional, func(id int64) []byte { _, hash := s.QRForUser(id); return hash })
			return err
		})
	} else {
		registration, validationErr := s.validateGoogleMerchant(req.MerchantRegistration)
		if validationErr != nil {
			return model.AuthData{}, validationErr
		}
		err = retryRegistration(ctx, func() error {
			location := model.BranchRegistrationLocation{
				BranchAddress: registration.BranchAddress, BranchLocality: registration.BranchLocality, BranchProvince: registration.BranchProvince,
				BranchPostalCode: registration.BranchPostalCode, BranchLatitude: registration.BranchLatitude, BranchLongitude: registration.BranchLongitude, PublicProfile: registration.PublicProfile,
			}
			u, err = s.Repo.CreateGoogleMerchant(ctx, googleID, email, name, lastName, registration.BrandName, registration.BranchName, location, registration.ProgramType, registration.ReferralCode)
			return err
		})
	}
	created := err == nil
	if errors.Is(err, repository.ErrEmailExists) || repository.IsUniqueViolation(err) {
		created = false
		u, err = s.Repo.ResolveGoogleUser(ctx, googleID, email)
	}
	if err != nil {
		return model.AuthData{}, err
	}
	data, err := s.googleSession(ctx, u)
	if err != nil {
		return model.AuthData{}, err
	}
	data.AccountCreated = created
	return data, nil
}

func (s *Service) validateGoogleMerchant(registration *model.GoogleMerchantRegistration) (model.GoogleMerchantRegistration, error) {
	if registration == nil {
		return model.GoogleMerchantRegistration{}, ErrInvalidRequest
	}
	brand, err := cleanName(registration.BrandName, 120)
	if err != nil {
		return model.GoogleMerchantRegistration{}, err
	}
	branch, err := cleanName(registration.BranchName, 120)
	if err != nil {
		return model.GoogleMerchantRegistration{}, err
	}
	location := model.BranchRegistrationLocation{
		BranchAddress: registration.BranchAddress, BranchLocality: registration.BranchLocality, BranchProvince: registration.BranchProvince,
		BranchPostalCode: registration.BranchPostalCode, BranchLatitude: registration.BranchLatitude, BranchLongitude: registration.BranchLongitude, PublicProfile: registration.PublicProfile,
	}
	if err = cleanRegistrationBranchLocation(&location); err != nil {
		return model.GoogleMerchantRegistration{}, err
	}
	programType, err := normalizeProgramType(registration.ProgramType)
	if err != nil {
		return model.GoogleMerchantRegistration{}, ErrInvalidRequest
	}
	return model.GoogleMerchantRegistration{
		BrandName: brand, BranchName: branch, BranchAddress: location.BranchAddress, BranchLocality: location.BranchLocality,
		BranchProvince: location.BranchProvince, BranchPostalCode: location.BranchPostalCode, BranchLatitude: location.BranchLatitude,
		BranchLongitude: location.BranchLongitude, PublicProfile: location.PublicProfile, ProgramType: programType, ReferralCode: strings.ToUpper(strings.TrimSpace(registration.ReferralCode)),
	}, nil
}

func (s *Service) googleSession(ctx context.Context, u model.User) (model.AuthData, error) {
	session, err := s.issueSession(ctx, u)
	if err != nil {
		return model.AuthData{}, err
	}
	return model.AuthData{Session: session, User: u}, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (model.AuthData, error) {
	if len(refreshToken) < 40 || len(refreshToken) > 256 {
		return model.AuthData{}, ErrInvalidCredentials
	}
	credentials, err := newSessionCredentials()
	if err != nil {
		return model.AuthData{}, err
	}
	oldHash := sha256.Sum256([]byte(refreshToken))
	rotated, err := s.Repo.RotateSession(ctx, oldHash[:], credentials.id, credentials.hash, credentials.expiresAt)
	if err != nil {
		if err == repository.ErrNotFound || err == repository.ErrSessionReuse {
			return model.AuthData{}, ErrInvalidCredentials
		}
		return model.AuthData{}, err
	}
	credentials.authTime = rotated.AuthTime
	session, err := s.session(rotated.User, credentials)
	if err != nil {
		return model.AuthData{}, err
	}
	return model.AuthData{Session: session, User: rotated.User}, nil
}

func (s *Service) Logout(ctx context.Context, userID int64, sessionID string) error {
	if err := s.Repo.RevokeSession(ctx, userID, sessionID); err != nil && err != repository.ErrNotFound {
		return err
	}
	return nil
}

func (s *Service) issueSession(ctx context.Context, u model.User) (model.Session, error) {
	credentials, err := newSessionCredentials()
	if err != nil {
		return model.Session{}, err
	}
	if err = s.Repo.CreateSession(ctx, credentials.id, u.ID, credentials.hash, credentials.expiresAt, credentials.authTime, u.AuthVersion); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.Session{}, ErrInvalidCredentials
		}
		return model.Session{}, err
	}
	return s.session(u, credentials)
}

func (s *Service) session(u model.User, credentials sessionCredentials) (model.Session, error) {
	token, err := s.Tokens.GenerateForSessionAtVersion(u.ID, u.AccountType, credentials.id, credentials.authTime, u.AuthVersion)
	if err != nil {
		return model.Session{}, err
	}
	return model.Session{AccessToken: token, RefreshToken: credentials.raw, TokenType: "Bearer", ExpiresIn: 900}, nil
}

func identityToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}
func parseIdentityToken(token string) ([]byte, error) {
	token = strings.TrimSpace(token)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, ErrIdentityToken
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], nil
}

func (s *Service) RequestEmailVerification(ctx context.Context, req model.EmailRequest) error {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return ErrInvalidRequest
	}
	if err = s.checkCaptcha(ctx, "identity"); err != nil {
		return err
	}
	token, hash, err := identityToken()
	if err != nil {
		return err
	}
	message := mailer.VerificationMessage(s.Config.PublicAppURL, email, token)
	return s.Repo.EnqueueVerification(ctx, email, hash, s.Now().Add(24*time.Hour), message)
}
func (s *Service) ConfirmEmailVerification(ctx context.Context, req model.TokenRequest) error {
	hash, err := parseIdentityToken(req.Token)
	if err != nil {
		return ErrIdentityToken
	}
	if err = s.Repo.VerifyEmail(ctx, hash, s.Now()); err == repository.ErrIdentityTokenInvalid {
		return ErrIdentityToken
	}
	return err
}

func (s *Service) RequestPasswordReset(ctx context.Context, req model.EmailRequest) error {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return ErrInvalidRequest
	}
	if err = s.checkCaptcha(ctx, "identity"); err != nil {
		return err
	}
	token, hash, err := identityToken()
	if err != nil {
		return err
	}
	message := mailer.PasswordResetMessage(s.Config.PublicAppURL, email, token)
	return s.Repo.EnqueuePasswordReset(ctx, email, hash, s.Now().Add(time.Hour), message)
}
func (s *Service) ConfirmPasswordReset(ctx context.Context, req model.PasswordResetConfirmRequest) error {
	if !validPassword(req.NewPassword) {
		return ErrInvalidRequest
	}
	hash, err := parseIdentityToken(req.Token)
	if err != nil {
		return ErrIdentityToken
	}
	if err = s.Repo.ValidatePasswordReset(ctx, hash, s.Now()); err != nil {
		if errors.Is(err, repository.ErrIdentityTokenInvalid) {
			return ErrIdentityToken
		}
		return err
	}
	passwordHash, err := password.Generate(ctx, []byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err = s.Repo.ResetPassword(ctx, hash, string(passwordHash), s.Now()); err == repository.ErrIdentityTokenInvalid {
		return ErrIdentityToken
	}
	return err
}

func newSessionCredentials() (sessionCredentials, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return sessionCredentials{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(encoded))
	now := time.Now().UTC()
	return sessionCredentials{id: uuid.NewString(), raw: encoded, hash: hash[:], expiresAt: now.Add(refreshLifetime), authTime: now}, nil
}

func (s *Service) LogoutRefresh(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if len(token) < 40 || len(token) > 256 {
		return ErrInvalidRequest
	}
	hash := sha256.Sum256([]byte(token))
	return s.Repo.RevokeRefreshFamily(ctx, hash[:])
}
