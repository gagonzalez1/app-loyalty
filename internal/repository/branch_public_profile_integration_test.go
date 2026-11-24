package repository_test

import (
	"os"
	"reflect"
	"testing"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
)

func TestPostgresBranchPublicProfileUpgradeAndEdits(t *testing.T) {
	p := referralBillingPool(t)
	ctx := t.Context()
	apply := func(filename string) {
		t.Helper()
		sql, err := os.ReadFile("../../migrations/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	apply("0046_branch_public_profile.down.sql")
	f := fixtureReviews(t, p)
	var before, after string
	if err := p.QueryRow(ctx, "SELECT to_jsonb(s)::text FROM sucursales s WHERE id=$1", f.branch).Scan(&before); err != nil {
		t.Fatal(err)
	}
	apply("0046_branch_public_profile.up.sql")
	var empty bool
	if err := p.QueryRow(ctx, "SELECT (to_jsonb(s)-'perfil_publico')::text, perfil_publico IS NULL FROM sucursales s WHERE id=$1", f.branch).Scan(&after, &empty); err != nil || before != after || !empty {
		t.Fatalf("existing branch changed: empty=%v before=%s after=%s err=%v", empty, before, after, err)
	}

	repo := repository.New(p)
	profile := &model.BranchPublicProfile{
		Hours: []model.BranchHoursSlot{{Days: []int{1, 2, 3}, Opens: "18:00", Closes: "01:00"}},
		Links: map[string]string{"instagram": "https://www.instagram.com/puntazo"},
	}
	added, err := repo.CreateBranch(ctx, f.owner, f.brand, model.CreateBranchRequest{Name: "New", PublicProfile: profile})
	if err != nil || !reflect.DeepEqual(added.PublicProfile, profile) {
		t.Fatalf("added=%+v err=%v", added, err)
	}
	legacyEdit, err := repo.UpdateBranch(ctx, f.owner, f.brand, added.ID, added.Version, model.UpdateBranchRequest{Name: "Renamed"})
	if err != nil || !reflect.DeepEqual(legacyEdit.PublicProfile, profile) {
		t.Fatalf("profile lost on legacy edit: %+v err=%v", legacyEdit, err)
	}
	catalog, err := repo.ListCustomerStoreLocations(ctx, f.customer, "Renamed")
	if err != nil || len(catalog) != 1 || !reflect.DeepEqual(catalog[0].PublicProfile, profile) || catalog[0].HasMenu {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	cleared, err := repo.UpdateBranch(ctx, f.owner, f.brand, added.ID, legacyEdit.Version, model.UpdateBranchRequest{Name: "Renamed", PublicProfile: &model.BranchPublicProfile{}})
	if err != nil || !reflect.DeepEqual(cleared.PublicProfile, &model.BranchPublicProfile{}) {
		t.Fatalf("profile could not be cleared: %+v err=%v", cleared, err)
	}
}
