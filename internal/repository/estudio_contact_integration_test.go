package repository_test

import (
	"errors"
	"testing"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
)

func TestPostgresEstudioContactIsVersionedAndPublic(t *testing.T) {
	pool := referralBillingPool(t)
	ctx := t.Context()
	f := fixtureReviews(t, pool)
	repo := repository.New(pool)
	branch, err := repo.CreateBranch(ctx, f.owner, f.brand, model.CreateBranchRequest{Name: "Ficha Estudio", PublicProfile: &model.BranchPublicProfile{
		Links: map[string]string{"facebook": "https://facebook.com/puntazo"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.EstudioContact(ctx, f.owner, f.brand, branch.ID)
	if err != nil || !before.CanWrite {
		t.Fatalf("owner cannot edit: %+v %v", before, err)
	}
	if _, err := repo.EstudioContact(ctx, f.customer, f.brand, branch.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("foreign profile lookup: %v", err)
	}
	whatsapp := "+5491112345678"
	addr := "Calle 100"
	changed, err := repo.UpdateEstudioContact(ctx, f.owner, f.brand, branch.ID, before.Version, model.EstudioContactPatch{WhatsApp: &whatsapp, Address: &addr})
	if err != nil || changed.Version != before.Version+1 || changed.PublicProfile.WhatsApp == nil || *changed.PublicProfile.WhatsApp != whatsapp || changed.PublicProfile.Links["facebook"] == "" {
		t.Fatalf("sparse update: %+v %v", changed, err)
	}
	if _, err := repo.UpdateEstudioContact(ctx, f.owner, f.brand, branch.ID, before.Version, model.EstudioContactPatch{Address: &addr}); !errors.Is(err, repository.ErrPreconditionFailed) {
		t.Fatalf("stale write: %v", err)
	}
	public, err := repo.PublicStore(ctx, branch.ID)
	if err != nil || public.PublicProfile.WhatsApp == nil || *public.PublicProfile.WhatsApp != whatsapp || public.Address == nil || *public.Address != addr {
		t.Fatalf("public store: %+v %v", public, err)
	}
	legacy, err := repo.UpdateBranch(ctx, f.owner, f.brand, branch.ID, changed.Version, model.UpdateBranchRequest{
		Name: branch.Name, Address: &addr, PublicProfile: &model.BranchPublicProfile{Links: map[string]string{"facebook": "https://facebook.com/puntazo"}},
	})
	if err != nil || legacy.PublicProfile.WhatsApp == nil || *legacy.PublicProfile.WhatsApp != whatsapp {
		t.Fatalf("older app update dropped WhatsApp: %+v %v", legacy, err)
	}
	changed.Version = legacy.Version
	empty := ""
	after, err := repo.UpdateEstudioContact(ctx, f.owner, f.brand, branch.ID, changed.Version, model.EstudioContactPatch{WhatsApp: &empty})
	if err != nil || after.PublicProfile.WhatsApp != nil || after.Address == nil {
		t.Fatalf("explicit clear: %+v %v", after, err)
	}
}
