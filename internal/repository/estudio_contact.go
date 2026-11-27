package repository

import (
	"context"
	"errors"

	"clientesFrecuentes/internal/model"
	"github.com/jackc/pgx/v5"
)

const estudioContactQuery = `SELECT s.id, s.version, s.direccion, s.perfil_publico, mm.rol IN ('PROPIETARIO','ADMINISTRADOR')
FROM sucursales s
JOIN marcas m ON m.id=s.marca_id AND m.activo AND m.deleted_at IS NULL
JOIN membresias_marca mm ON mm.marca_id=m.id AND mm.usuario_id=$1 AND mm.activo
WHERE s.marca_id=$2 AND s.id=$3 AND s.activo AND s.deleted_at IS NULL
AND (mm.rol IN ('PROPIETARIO','ADMINISTRADOR') OR EXISTS (
    SELECT 1 FROM membresias_sucursales ms WHERE ms.membresia_id=mm.id AND ms.sucursal_id=s.id AND ms.activo
))`

func scanEstudioContact(row rowScanner) (model.EstudioContact, error) {
	var contact model.EstudioContact
	err := row.Scan(&contact.BranchID, &contact.Version, &contact.Address, &contact.PublicProfile, &contact.CanWrite)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return contact, err
}

func (r *Repository) EstudioContact(ctx context.Context, actor, brand, branch int64) (model.EstudioContact, error) {
	return scanEstudioContact(r.Pool.QueryRow(ctx, estudioContactQuery, actor, brand, branch))
}

func (r *Repository) UpdateEstudioContact(ctx context.Context, actor, brand, branch int64, version int, patch model.EstudioContactPatch) (model.EstudioContact, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.EstudioContact{}, err
	}
	defer tx.Rollback(ctx)
	current, err := scanEstudioContact(tx.QueryRow(ctx, estudioContactQuery+" FOR UPDATE OF m, mm, s", actor, brand, branch))
	if err != nil {
		return model.EstudioContact{}, err
	}
	if !current.CanWrite {
		return model.EstudioContact{}, ErrForbidden
	}
	if current.Version != version {
		return model.EstudioContact{}, ErrPreconditionFailed
	}
	if patch.Address != nil {
		current.Address = patch.Address
		if *patch.Address == "" {
			current.Address = nil
		}
	}
	if current.PublicProfile == nil {
		current.PublicProfile = &model.BranchPublicProfile{}
	}
	if patch.Hours != nil {
		current.PublicProfile.Hours = *patch.Hours
	}
	if patch.WhatsApp != nil {
		current.PublicProfile.WhatsApp = patch.WhatsApp
		if *patch.WhatsApp == "" {
			current.PublicProfile.WhatsApp = nil
		}
	}
	for key, value := range map[string]*string{"instagram": patch.Instagram, "tiktok": patch.TikTok} {
		if value == nil {
			continue
		}
		if *value == "" {
			delete(current.PublicProfile.Links, key)
		} else {
			if current.PublicProfile.Links == nil {
				current.PublicProfile.Links = make(map[string]string)
			}
			current.PublicProfile.Links[key] = *value
		}
	}
	err = tx.QueryRow(ctx, `UPDATE sucursales SET direccion=$3, perfil_publico=$4, version=version+1,
		updated_at=now() WHERE id=$1 AND marca_id=$2 RETURNING version`,
		branch, brand, current.Address, current.PublicProfile).Scan(&current.Version)
	if err != nil {
		return model.EstudioContact{}, err
	}
	return current, tx.Commit(ctx)
}
