package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEstudioCredentialIsScopedAndExpires(t *testing.T) {
	now := time.Now().UTC()
	tokens := NewTokens("secret-only-for-tests", "testing")
	tokens.Now = func() time.Time { return now }
	session := uuid.NewString()
	grant, expires, err := tokens.GenerateEstudio(4, 8, 12, session, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	user, claims, err := tokens.ParseEstudio(grant)
	if err != nil || user != 4 || claims.BrandID != 8 || claims.BranchID != 12 || claims.ID != session || claims.AuthVersion != 3 {
		t.Fatalf("incorrect grant scope: user=%d claims=%+v err=%v", user, claims, err)
	}
	if _, _, err := tokens.Parse(grant); err == nil {
		t.Fatal("Estudio credential accepted as a Puntazo app credential")
	}
	access, err := tokens.GenerateForSession(4, "PERSONAL_MARCA", session)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tokens.ParseEstudio(access); err == nil {
		t.Fatal("ordinary access token accepted as Estudio delegation")
	}
	now = expires.Add(time.Second)
	if _, _, err := tokens.ParseEstudio(grant); err == nil {
		t.Fatal("expired grant accepted")
	}
}
