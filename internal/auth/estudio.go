package auth

import (
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const estudioAudience = "puntazo-estudio-contact"

type EstudioClaims struct {
	Claims
	BrandID  int64 `json:"brand_id"`
	BranchID int64 `json:"branch_id"`
}

func (t *Tokens) GenerateEstudio(userID, brandID, branchID int64, sessionID string, authVersion int, authTime time.Time) (string, time.Time, error) {
	now := t.Now().UTC()
	expires := now.Add(2 * time.Hour).Truncate(time.Second)
	claims := EstudioClaims{
		BrandID: brandID, BranchID: branchID,
		Claims: Claims{AccountType: "PERSONAL_MARCA", AuthTime: authTime.Unix(), AuthVersion: authVersion,
			RegisteredClaims: jwt.RegisteredClaims{Subject: strconv.FormatInt(userID, 10), ID: sessionID,
				Issuer: t.Issuer, Audience: []string{estudioAudience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expires)}},
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.Secret)
	return raw, expires, err
}

func (t *Tokens) ParseEstudio(raw string) (int64, EstudioClaims, error) {
	var claims EstudioClaims
	token, err := jwt.ParseWithClaims(raw, &claims, func(_ *jwt.Token) (any, error) { return t.Secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience(estudioAudience), jwt.WithIssuer(t.Issuer),
		jwt.WithExpirationRequired(), jwt.WithTimeFunc(t.Now))
	id, parseErr := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || !token.Valid || parseErr != nil || id < 1 || claims.BrandID < 1 || claims.BranchID < 1 ||
		len(claims.Audience) != 1 || claims.AuthVersion < 1 || uuid.Validate(claims.ID) != nil || claims.AccountType != "PERSONAL_MARCA" {
		return 0, EstudioClaims{}, ErrInvalidToken
	}
	return id, claims, nil
}
