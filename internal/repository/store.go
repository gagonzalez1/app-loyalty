package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"clientesFrecuentes/internal/model"
	"github.com/jackc/pgx/v5"
)

// Escape LIKE metacharacters so search remains a literal substring, including
// percent signs and backslashes in real merchant names and addresses.
func storeSearchPattern(search string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search) + "%"
}

// CustomerStoreReviewSettings is a read-only discovery lookup. Invitation
// enablement, cards, purchases and subscriptions do not gate this manual action.
func (r *Repository) CustomerStoreReviewSettings(ctx context.Context, actorID, branchID int64) (model.ReviewSettings, error) {
	var kind string
	err := r.Pool.QueryRow(ctx, `SELECT tipo_cuenta FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL`, actorID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ReviewSettings{}, ErrNotFound
	}
	if err != nil {
		return model.ReviewSettings{}, err
	}
	if kind != "CLIENTE_FINAL" {
		return model.ReviewSettings{}, ErrForbidden
	}
	var out model.ReviewSettings
	err = r.Pool.QueryRow(ctx, `SELECT s.id,COALESCE(c.destination_type,'MANUAL_LINK'),c.google_place_id,c.manual_review_url
		FROM sucursales s JOIN marcas m ON m.id=s.marca_id AND m.activo AND m.deleted_at IS NULL
		LEFT JOIN configuracion_resenas c ON c.sucursal_id=s.id
		WHERE s.id=$1 AND s.activo AND s.deleted_at IS NULL`, branchID).
		Scan(&out.BranchID, &out.DestinationType, &out.GooglePlaceID, &out.ManualReviewURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ReviewSettings{}, ErrNotFound
	}
	return out, err
}

func (r *Repository) ListCustomerStoreLocations(ctx context.Context, actorID int64, search string) ([]model.StoreLocation, error) {
	var kind string
	err := r.Pool.QueryRow(ctx, `SELECT tipo_cuenta FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL`, actorID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if kind != "CLIENTE_FINAL" {
		return nil, ErrForbidden
	}
	rows, err := r.Pool.Query(ctx, `SELECT m.id,m.nombre,s.id,s.nombre,s.direccion,s.localidad,s.provincia,
 CASE WHEN s.latitud BETWEEN -90 AND 90 AND s.longitud BETWEEN -180 AND 180 THEN s.latitud END,
 CASE WHEN s.latitud BETWEEN -90 AND 90 AND s.longitud BETWEEN -180 AND 180 THEN s.longitud END,
 m.descripcion,s.perfil_publico,
 EXISTS(SELECT 1 FROM branch_menus bm JOIN branch_menu_documents d ON d.id=bm.published_document_id AND d.estado='READY' WHERE bm.sucursal_id=s.id AND bm.marca_id=m.id AND bm.language='es'),
 EXISTS(SELECT 1 FROM branch_menus bm JOIN branch_menu_documents d ON d.id=bm.published_document_id AND d.estado='READY' WHERE bm.sucursal_id=s.id AND bm.marca_id=m.id AND bm.language='en'),
 COALESCE((SELECT a.object_key FROM archivos_marca a WHERE a.marca_id=m.id AND a.tipo='LOGO' AND a.estado='ACTIVA' ORDER BY a.created_at DESC,a.id DESC LIMIT 1),''),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',b.id,'nombre',b.nombre,'descripcion',b.descripcion,'required_stamps',b.requisito_sellos,'required_points',b.requisito_puntos) ORDER BY b.id)
 FROM beneficios b JOIN programas_fidelidad p ON p.id=b.programa_id AND p.activo
 WHERE p.marca_id=m.id AND b.activo AND b.deleted_at IS NULL AND (b.expires_on IS NULL OR b.expires_on >= (now() AT TIME ZONE 'America/Argentina/Buenos_Aires')::date)),'[]'::jsonb),
 EXISTS(SELECT 1 FROM sucursales_favoritas f WHERE f.usuario_id=$1 AND f.sucursal_id=s.id),
 (SELECT count(*) FROM sucursales_favoritas f JOIN usuarios u ON u.id=f.usuario_id AND u.activo AND u.deleted_at IS NULL AND u.tipo_cuenta='CLIENTE_FINAL' WHERE f.sucursal_id=s.id)
 FROM marcas m JOIN sucursales s ON s.marca_id=m.id AND s.activo AND s.deleted_at IS NULL
 WHERE m.activo AND m.deleted_at IS NULL AND
 ($2='%%' OR m.nombre ILIKE $2 ESCAPE E'\\' OR s.nombre ILIKE $2 ESCAPE E'\\' OR s.localidad ILIKE $2 ESCAPE E'\\' OR s.direccion ILIKE $2 ESCAPE E'\\')
 ORDER BY lower(m.nombre),lower(s.nombre),m.id,s.id`, actorID, storeSearchPattern(search))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.StoreLocation{}
	for rows.Next() {
		var item model.StoreLocation
		var benefits []byte
		if err = rows.Scan(&item.BrandID, &item.BrandName, &item.BranchID, &item.BranchName, &item.Address, &item.Locality, &item.Province, &item.Latitude, &item.Longitude, &item.Description, &item.PublicProfile, &item.HasMenu, &item.HasEnglishMenu, &item.LogoObjectKey, &benefits, &item.Favorite, &item.Favorites); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(benefits, &item.Benefits); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) SetCustomerStoreFavorite(ctx context.Context, actorID, branchID int64, favorite bool) (model.StoreFavorite, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.StoreFavorite{}, err
	}
	defer tx.Rollback(ctx)
	var kind string
	err = tx.QueryRow(ctx, `SELECT tipo_cuenta FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL FOR SHARE`, actorID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoreFavorite{}, ErrNotFound
	}
	if err != nil {
		return model.StoreFavorite{}, err
	}
	if kind != "CLIENTE_FINAL" {
		return model.StoreFavorite{}, ErrForbidden
	}
	var brandID int64
	// Resolve identity without taking a branch lock. Commercial writes acquire
	// the brand before its branches, so favorites must preserve that same order.
	err = tx.QueryRow(ctx, `SELECT marca_id FROM sucursales WHERE id=$1`, branchID).Scan(&brandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoreFavorite{}, ErrNotFound
	}
	if err != nil {
		return model.StoreFavorite{}, err
	}
	var lockedID int64
	err = tx.QueryRow(ctx, `SELECT id FROM marcas WHERE id=$1 AND activo AND deleted_at IS NULL FOR SHARE`, brandID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoreFavorite{}, ErrNotFound
	}
	if err != nil {
		return model.StoreFavorite{}, err
	}
	// Revalidate the branch and its brand relationship after taking the brand
	// lock; serialize PUT/DELETE and retain eligibility until the count commits.
	err = tx.QueryRow(ctx, `SELECT id FROM sucursales WHERE id=$1 AND marca_id=$2 AND activo AND deleted_at IS NULL FOR UPDATE`, branchID, brandID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.StoreFavorite{}, ErrNotFound
	}
	if err != nil {
		return model.StoreFavorite{}, err
	}
	if favorite {
		_, err = tx.Exec(ctx, `INSERT INTO sucursales_favoritas(usuario_id,sucursal_id) VALUES($1,$2) ON CONFLICT(usuario_id,sucursal_id) DO NOTHING`, actorID, branchID)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM sucursales_favoritas WHERE usuario_id=$1 AND sucursal_id=$2`, actorID, branchID)
	}
	if err != nil {
		return model.StoreFavorite{}, err
	}
	out := model.StoreFavorite{BranchID: branchID, Favorite: favorite}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM sucursales_favoritas f JOIN usuarios u ON u.id=f.usuario_id AND u.activo AND u.deleted_at IS NULL AND u.tipo_cuenta='CLIENTE_FINAL' WHERE f.sucursal_id=$1`, branchID).Scan(&out.Favorites)
	if err != nil {
		return model.StoreFavorite{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.StoreFavorite{}, err
	}
	return out, nil
}
