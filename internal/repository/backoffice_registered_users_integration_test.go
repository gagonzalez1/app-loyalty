package repository_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"clientesFrecuentes/internal/repository"
)

func TestPostgresRegisteredUsersPaginationSearchAndPrivacy(t *testing.T) {
	pool := referralBillingPool(t)
	ctx := t.Context()
	repo := repository.New(pool)
	empty, err := repo.ListBackofficeRegisteredUsers(ctx, "", "", 2, 0)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	for _, fixture := range []struct {
		email, name, kind         string
		active, verified, deleted bool
	}{
		{"first@example.test", "Ana", "CLIENTE_FINAL", true, true, false},
		{"second@example.test", "Bruno", "PERSONAL_MARCA", false, false, false},
		{"third@example.test", "Carla", "CLIENTE_FINAL", true, false, false},
		{"deleted@example.test", "Deleted", "PERSONAL_MARCA", false, false, true},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO usuarios(email,nombre,apellido,tipo_cuenta,qr_hash,password_hash,activo,email_verified_at,deleted_at)
 VALUES($1::text,$2,'Prueba',$3,CASE WHEN $3='CLIENTE_FINAL' THEN decode(md5($1::text),'hex') END,'NEVER_EXPOSE_SECRET',$4,CASE WHEN $5 THEN now() END,CASE WHEN $6 THEN now() END)`, fixture.email, fixture.name, fixture.kind, fixture.active, fixture.verified, fixture.deleted)
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.ListBackofficeRegisteredUsers(ctx, "", "", 2, 0)
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil || first.Items[0].Name != "Carla" || first.Items[1].Active {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	before, _ := strconv.ParseInt(*first.NextCursor, 10, 64)
	last, err := repo.ListBackofficeRegisteredUsers(ctx, "", "", 2, before)
	if err != nil || len(last.Items) != 1 || last.Items[0].Name != "Ana" || last.Items[0].EmailVerifiedAt == nil || last.NextCursor != nil {
		t.Fatalf("last=%+v err=%v", last, err)
	}
	for _, test := range []struct {
		query, kind string
		count       int
	}{
		{"SECOND@EXAMPLE", "", 1}, {"prUEba", "CLIENTE_FINAL", 2}, {"", "PERSONAL_MARCA", 1}, {"%", "", 0}, {"deleted", "", 0}, {"' OR 1=1 --", "", 0},
	} {
		page, err := repo.ListBackofficeRegisteredUsers(ctx, test.query, test.kind, 50, 0)
		if err != nil || len(page.Items) != test.count {
			t.Fatalf("query=%q kind=%q count=%d err=%v", test.query, test.kind, len(page.Items), err)
		}
	}
	encoded, _ := json.Marshal(first)
	for _, forbidden := range []string{"password", "qr_hash", "google_id", "NEVER_EXPOSE_SECRET", "deleted@example"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("sensitive field %s exposed", forbidden)
		}
	}
	if _, err := repo.ListBackofficeRegisteredUsers(ctx, "", "ADMIN_SISTEMA", 50, 0); err != repository.ErrInvalidRequest {
		t.Fatalf("invalid account type: %v", err)
	}
}
