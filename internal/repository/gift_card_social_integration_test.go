package repository_test

import (
	"errors"
	"os"
	"slices"
	"testing"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"

	"github.com/google/uuid"
)

func TestPostgresGiftCardSocialSnapshot(t *testing.T) {
	pool, repo, svc := codeFixture(t)
	ctx := t.Context()
	merchant := fixtureReviews(t, pool)
	apply := func(direction string) {
		t.Helper()
		sql, err := os.ReadFile("../../migrations/0055_gift_card_social_links." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}

	apply("down")
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version='0055'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckSchema(ctx, "0054"); err != nil {
		t.Fatal(err)
	}
	legacyID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO gift_card_batches(id,marca_id,sucursal_id,programa_id,creado_por,titulo,programa_tipo,cantidad,total_codigos)
		VALUES($1,$2,$3,$4,$5,'Lote anterior','PUNTOS',5,1)`, legacyID, merchant.brand, merchant.branch, merchant.program, merchant.owner); err != nil {
		t.Fatal(err)
	}
	apply("up")
	if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0055')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckSchema(ctx, "0055"); err != nil {
		t.Fatal(err)
	}
	legacy, err := svc.GiftCardBatch(ctx, merchant.owner, merchant.brand, legacyID)
	if err != nil || legacy.Title != "Lote anterior" || len(legacy.SocialLinks) != 0 {
		t.Fatalf("legacy batch=%+v err=%v", legacy, err)
	}

	branch, err := svc.Branch(ctx, merchant.owner, merchant.brand, merchant.branch)
	if err != nil {
		t.Fatal(err)
	}
	updateLinks := func(links map[string]string) {
		t.Helper()
		branch, err = svc.UpdateBranch(ctx, merchant.owner, merchant.brand, merchant.branch, branch.Version, model.UpdateBranchRequest{
			Name: branch.Name, Address: branch.Address, PublicProfile: &model.BranchPublicProfile{Links: links},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	instagram := "https://www.instagram.com/original"
	tiktok := "https://www.tiktok.com/@original"
	updateLinks(map[string]string{"instagram": instagram, "tiktok": tiktok, "facebook": "https://www.facebook.com/extra"})

	request := model.GiftCardCreateRequest{Title: "Un regalo", BranchID: branch.ID, Amount: 5, Quantity: 2, SocialNetworks: []string{"tiktok", "instagram"}}
	if _, err = svc.CreateGiftCardBatch(ctx, merchant.operator, merchant.brand, request); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("operator without branch access = %v", err)
	}
	batch, err := svc.CreateGiftCardBatch(ctx, merchant.owner, merchant.brand, request)
	expected := []model.GiftCardSocialLink{{Network: "instagram", URL: instagram}, {Network: "tiktok", URL: tiktok}}
	if err != nil || len(batch.Codes) != 2 || !slices.Equal(batch.SocialLinks, expected) {
		t.Fatalf("created social links=%+v codes=%d err=%v", batch.SocialLinks, len(batch.Codes), err)
	}

	updateLinks(map[string]string{"instagram": "https://www.instagram.com/nuevo"})
	if _, err = svc.CreateGiftCardBatch(ctx, merchant.owner, merchant.brand, request); !errors.Is(err, repository.ErrGiftCardSocialUnavailable) {
		t.Fatalf("stale selection = %v", err)
	}
	batches, err := svc.ListGiftCardBatches(ctx, merchant.owner, merchant.brand)
	if err != nil || len(batches) != 2 {
		t.Fatalf("rejected selection left a batch, count=%d err=%v", len(batches), err)
	}
	detail, err := svc.GiftCardBatch(ctx, merchant.owner, merchant.brand, batch.ID)
	if err != nil || !slices.Equal(detail.SocialLinks, expected) || len(detail.Codes) != len(batch.Codes) {
		t.Fatalf("reprinted social links=%+v codes=%d err=%v", detail.SocialLinks, len(detail.Codes), err)
	}
}
