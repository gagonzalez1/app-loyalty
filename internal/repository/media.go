package repository

import (
	"context"
	"encoding/hex"
	"errors"

	"clientesFrecuentes/internal/model"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func scanBrandImage(row rowScanner, item *model.BrandImage) error {
	var digest []byte
	err := row.Scan(&item.ID, &item.BrandID, &item.Type, &item.BenefitID, &item.ObjectKey, &item.MIMEType, &item.ByteSize, &digest, &item.Width, &item.Height, &item.Status, &item.Version, &item.CreatedAt, &item.UpdatedAt)
	item.SHA256 = hex.EncodeToString(digest)
	return err
}

func (r *Repository) AuthorizeBrandMedia(ctx context.Context, actorID, brandID int64) error {
	var role string
	err := r.Pool.QueryRow(ctx, `SELECT mm.rol FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id AND m.activo AND m.deleted_at IS NULL WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo`, actorID, brandID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !mutableRole(role) {
		return ErrForbidden
	}
	return nil
}

func (r *Repository) ReserveBrandImage(ctx context.Context, actorID, brandID int64, item model.BrandImage, digest []byte) (model.BrandImage, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return item, err
	}
	defer tx.Rollback(ctx)
	var role string
	if err = tx.QueryRow(ctx, `SELECT mm.rol FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id AND m.activo WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo FOR UPDATE OF m`, actorID, brandID).Scan(&role); errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	} else if err != nil {
		return item, err
	}
	if !mutableRole(role) {
		return item, ErrForbidden
	}
	var programID *int64
	if item.Type == "BENEFICIO" {
		if item.BenefitID == nil {
			return item, ErrInvalidRequest
		}
		var id int64
		if err = tx.QueryRow(ctx, `SELECT p.id FROM beneficios b JOIN programas_fidelidad p ON p.id=b.programa_id AND p.marca_id=$1 WHERE b.id=$2 AND b.activo AND p.activo`, brandID, *item.BenefitID).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
			return item, ErrNotFound
		} else if err != nil {
			return item, err
		}
		programID = &id
	} else if item.BenefitID != nil {
		return item, ErrInvalidRequest
	}
	var replaces *string
	if item.Type == "BENEFICIO" {
		_ = tx.QueryRow(ctx, `SELECT id FROM archivos_marca WHERE marca_id=$1 AND beneficio_id=$2 AND estado='ACTIVA' FOR UPDATE`, brandID, item.BenefitID).Scan(&replaces)
	} else {
		_ = tx.QueryRow(ctx, `SELECT id FROM archivos_marca WHERE marca_id=$1 AND tipo=$2 AND estado='ACTIVA' FOR UPDATE`, brandID, item.Type).Scan(&replaces)
	}
	err = scanBrandImage(tx.QueryRow(ctx, `INSERT INTO archivos_marca(id,marca_id,tipo,beneficio_id,programa_id,object_key,mime_type,byte_size,sha256,width,height,estado,replaces_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'UPLOAD_PENDING',$12) RETURNING id,marca_id,tipo,beneficio_id,object_key,mime_type,byte_size,sha256,width,height,estado,version,created_at,updated_at`, item.ID, brandID, item.Type, item.BenefitID, programID, item.ObjectKey, item.MIMEType, item.ByteSize, digest, item.Width, item.Height, replaces), &item)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return item, ErrConflict
	}
	if err != nil {
		return item, err
	}
	return item, tx.Commit(ctx)
}

func (r *Repository) ActivateBrandImage(ctx context.Context, actorID int64, id string) (model.BrandImage, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.BrandImage{}, err
	}
	defer tx.Rollback(ctx)
	var brandID int64
	var replaces *string
	if err = tx.QueryRow(ctx, `SELECT a.marca_id,a.replaces_id FROM archivos_marca a JOIN membresias_marca mm ON mm.marca_id=a.marca_id AND mm.usuario_id=$1 AND mm.activo AND mm.rol IN ('PROPIETARIO','ADMINISTRADOR') JOIN marcas m ON m.id=a.marca_id AND m.activo WHERE a.id=$2 AND a.estado='UPLOAD_PENDING' FOR UPDATE OF m`, actorID, id).Scan(&brandID, &replaces); errors.Is(err, pgx.ErrNoRows) {
		return model.BrandImage{}, ErrNotFound
	} else if err != nil {
		return model.BrandImage{}, err
	}
	if replaces != nil {
		if _, err = tx.Exec(ctx, `UPDATE archivos_marca SET estado='DELETE_PENDING',delete_after=now()+interval '24 hours',version=version+1,updated_at=now() WHERE id=$1 AND estado='ACTIVA'`, *replaces); err != nil {
			return model.BrandImage{}, err
		}
	}
	var item model.BrandImage
	err = scanBrandImage(tx.QueryRow(ctx, `UPDATE archivos_marca SET estado='ACTIVA',uploaded_at=now(),updated_at=now() WHERE id=$1 AND estado='UPLOAD_PENDING' RETURNING id,marca_id,tipo,beneficio_id,object_key,mime_type,byte_size,sha256,width,height,estado,version,created_at,updated_at`, id), &item)
	if err != nil {
		return item, err
	}
	return item, tx.Commit(ctx)
}

func (r *Repository) FailBrandImage(ctx context.Context, id string, failure string) {
	_, _ = r.Pool.Exec(ctx, `UPDATE archivos_marca SET estado='UPLOAD_FAILED',delete_after=now(),last_error=left($2,1000),updated_at=now() WHERE id=$1 AND estado='UPLOAD_PENDING'`, id, failure)
}

func (r *Repository) ListBrandImages(ctx context.Context, actorID, brandID int64) ([]model.BrandImage, error) {
	var allowed bool
	if err := r.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id AND m.activo WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo)`, actorID, brandID).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	rows, err := r.Pool.Query(ctx, `SELECT id,marca_id,tipo,beneficio_id,object_key,mime_type,byte_size,sha256,width,height,estado,version,created_at,updated_at FROM archivos_marca WHERE marca_id=$1 AND estado='ACTIVA' ORDER BY tipo,created_at,id`, brandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.BrandImage{}
	for rows.Next() {
		var item model.BrandImage
		if err = scanBrandImage(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) DeleteBrandImage(ctx context.Context, actorID, brandID int64, id string, version int) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE archivos_marca a SET estado='DELETE_PENDING',delete_after=now()+interval '24 hours',version=a.version+1,updated_at=now() FROM membresias_marca mm WHERE a.id=$1 AND a.marca_id=$2 AND a.version=$3 AND a.estado='ACTIVA' AND mm.marca_id=a.marca_id AND mm.usuario_id=$4 AND mm.activo AND mm.rol IN ('PROPIETARIO','ADMINISTRADOR')`, id, brandID, version, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var own bool
	if err = r.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM archivos_marca a JOIN membresias_marca mm ON mm.marca_id=a.marca_id AND mm.usuario_id=$1 AND mm.activo AND mm.rol IN ('PROPIETARIO','ADMINISTRADOR') WHERE a.id=$2 AND a.marca_id=$3 AND a.estado='ACTIVA')`, actorID, id, brandID).Scan(&own); err != nil {
		return err
	}
	if own {
		return ErrPreconditionFailed
	}
	return ErrNotFound
}

type MediaDeletion struct{ ID, ObjectKey, Status string }

func (r *Repository) DueBrandMedia(ctx context.Context, limit int, leaseOwner string) ([]MediaDeletion, error) {
	rows, err := r.Pool.Query(ctx, `UPDATE archivos_marca SET lease_owner=$2,lease_until=now()+interval '5 minutes',updated_at=now() WHERE id IN(SELECT id FROM archivos_marca WHERE (lease_until IS NULL OR lease_until<now()) AND COALESCE(delete_after,now())<=now() AND ((estado='DELETE_PENDING' AND delete_after<=now()) OR estado='UPLOAD_FAILED' OR (estado='UPLOAD_PENDING' AND created_at<now()-interval '15 minutes')) ORDER BY COALESCE(delete_after,created_at),id LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING id,object_key,estado`, limit, leaseOwner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MediaDeletion{}
	for rows.Next() {
		var x MediaDeletion
		if err = rows.Scan(&x.ID, &x.ObjectKey, &x.Status); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (r *Repository) CompleteBrandMediaDeletion(ctx context.Context, id, leaseOwner, errText string) error {
	var tag pgconn.CommandTag
	var err error
	if errText != "" {
		tag, err = r.Pool.Exec(ctx, `UPDATE archivos_marca SET delete_after=now()+interval '1 hour',lease_owner=NULL,lease_until=NULL,last_error=left($3,1000),updated_at=now() WHERE id=$1 AND lease_owner=$2 AND estado IN ('DELETE_PENDING','UPLOAD_PENDING','UPLOAD_FAILED')`, id, leaseOwner, errText)
	} else {
		tag, err = r.Pool.Exec(ctx, `UPDATE archivos_marca SET estado='DELETED',delete_after=NULL,object_key='deleted/'||id::text,lease_owner=NULL,lease_until=NULL,last_error=NULL,deleted_at=now(),updated_at=now() WHERE id=$1 AND lease_owner=$2 AND estado IN ('DELETE_PENDING','UPLOAD_PENDING','UPLOAD_FAILED')`, id, leaseOwner)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
