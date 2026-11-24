package repository

import (
	"context"
	"errors"

	"clientesFrecuentes/internal/model"
	"github.com/jackc/pgx/v5"
)

type MenuStateSigner func(context.Context, model.BranchMenuState) (model.BranchMenuState, error)

func (r *Repository) AuthorizeBranchMenu(ctx context.Context, actor, brand, branch int64) error {
	var role string
	err := r.Pool.QueryRow(ctx, `SELECT mm.rol FROM membresias_marca mm JOIN usuarios u ON u.id=mm.usuario_id AND u.activo AND u.deleted_at IS NULL AND u.tipo_cuenta='PERSONAL_MARCA' JOIN marcas m ON m.id=mm.marca_id AND m.activo AND m.deleted_at IS NULL JOIN sucursales s ON s.marca_id=m.id AND s.id=$3 AND s.activo AND s.deleted_at IS NULL WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo`, actor, brand, branch).Scan(&role)
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

// Acquire resources in the same order as commercial editing: brand, branch,
// menu. Authorization is rechecked inside every mutating transaction.
func (r *Repository) lockMenu(ctx context.Context, tx pgx.Tx, actor, brand, branch int64, write bool) error {
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM marcas WHERE id=$1 AND activo AND deleted_at IS NULL FOR SHARE`, brand).Scan(&id); err != nil {
		return menuNotFound(err)
	}
	q := `SELECT id FROM sucursales WHERE id=$1 AND marca_id=$2 AND activo AND deleted_at IS NULL FOR SHARE`
	if write {
		q = `SELECT id FROM sucursales WHERE id=$1 AND marca_id=$2 AND activo AND deleted_at IS NULL FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, q, branch, brand).Scan(&id); err != nil {
		return menuNotFound(err)
	}
	var role string
	err := tx.QueryRow(ctx, `SELECT mm.rol FROM membresias_marca mm JOIN usuarios u ON u.id=mm.usuario_id AND u.activo AND u.deleted_at IS NULL AND u.tipo_cuenta='PERSONAL_MARCA' WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo`, actor, brand).Scan(&role)
	if err != nil {
		return menuNotFound(err)
	}
	if !mutableRole(role) {
		return ErrForbidden
	}
	return nil
}
func menuNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func readMenuDocument(ctx context.Context, tx pgx.Tx, id string) (*model.MenuDocument, error) {
	var d model.MenuDocument
	err := tx.QueryRow(ctx, `SELECT id::text,file_name,mime_type,byte_size,page_count,created_at,object_key FROM branch_menu_documents WHERE id=$1 AND estado='READY'`, id).Scan(&d.ID, &d.FileName, &d.MIMEType, &d.ByteSize, &d.PageCount, &d.CreatedAt, &d.ObjectKey)
	if err != nil {
		return nil, menuNotFound(err)
	}
	return &d, nil
}
func readMenuState(ctx context.Context, tx pgx.Tx, brand, branch int64, language model.MenuLanguage) (model.BranchMenuState, error) {
	out := model.BranchMenuState{BrandID: brand, BranchID: branch, Version: 1}
	var draft, published *string
	err := tx.QueryRow(ctx, `SELECT version,draft_document_id::text,published_document_id::text FROM branch_menus WHERE sucursal_id=$1 AND marca_id=$2 AND language=$3`, branch, brand, language).Scan(&out.Version, &draft, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if draft != nil {
		out.Draft, err = readMenuDocument(ctx, tx, *draft)
		if err != nil {
			return out, err
		}
	}
	if published != nil {
		out.Published, err = readMenuDocument(ctx, tx, *published)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
func finishMenu(ctx context.Context, tx pgx.Tx, brand, branch int64, language model.MenuLanguage, sign MenuStateSigner) (model.BranchMenuState, error) {
	out, err := readMenuState(ctx, tx, brand, branch, language)
	if err != nil {
		return out, err
	}
	if sign != nil {
		out, err = sign(ctx, out)
		if err != nil {
			return model.BranchMenuState{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return model.BranchMenuState{}, err
	}
	return out, nil
}
func (r *Repository) BranchMenu(ctx context.Context, actor, brand, branch int64, language model.MenuLanguage, sign MenuStateSigner) (model.BranchMenuState, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.BranchMenuState{}, err
	}
	defer tx.Rollback(ctx)
	if err = r.lockMenu(ctx, tx, actor, brand, branch, false); err != nil {
		return model.BranchMenuState{}, err
	}
	return finishMenu(ctx, tx, brand, branch, language, sign)
}
func (r *Repository) ReserveMenuDocument(ctx context.Context, actor, brand, branch int64, language model.MenuLanguage, version int, d model.MenuDocument, digest []byte) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = r.lockMenu(ctx, tx, actor, brand, branch, true); err != nil {
		return err
	}
	state, err := readMenuState(ctx, tx, brand, branch, language)
	if err != nil {
		return err
	}
	if state.Version != version {
		return ErrPreconditionFailed
	}
	_, err = tx.Exec(ctx, `INSERT INTO branch_menu_documents(id,marca_id,sucursal_id,file_name,byte_size,page_count,sha256,object_key,language) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, d.ID, brand, branch, d.FileName, d.ByteSize, d.PageCount, digest, d.ObjectKey, language)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func retireMenuDocument(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE branch_menu_documents SET estado='DELETE_PENDING',delete_after=clock_timestamp()+interval '6 minutes',lease_owner=NULL,lease_until=NULL WHERE id=$1 AND estado IN ('UPLOAD_PENDING','READY')`, id)
	return err
}

// The reserved immutable upload remains detached until storage succeeds. A stale
// version or signing failure rolls this transaction back, keeping the old PDF.
func (r *Repository) ActivateMenuDraft(ctx context.Context, actor, brand, branch int64, language model.MenuLanguage, version int, document string, sign MenuStateSigner) (model.BranchMenuState, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.BranchMenuState{}, err
	}
	defer tx.Rollback(ctx)
	if err = r.lockMenu(ctx, tx, actor, brand, branch, true); err != nil {
		return model.BranchMenuState{}, err
	}
	state, err := readMenuState(ctx, tx, brand, branch, language)
	if err != nil {
		return state, err
	}
	if state.Version != version {
		return state, ErrPreconditionFailed
	}
	tag, err := tx.Exec(ctx, `UPDATE branch_menu_documents SET estado='READY' WHERE id=$1 AND marca_id=$2 AND sucursal_id=$3 AND estado='UPLOAD_PENDING' AND lease_owner IS NULL AND language=$4`, document, brand, branch, language)
	if err != nil {
		return state, err
	}
	if tag.RowsAffected() != 1 {
		return state, ErrNotFound
	}
	_, err = tx.Exec(ctx, `INSERT INTO branch_menus(sucursal_id,marca_id,version,draft_document_id,language) VALUES($1,$2,2,$3,$4) ON CONFLICT(sucursal_id,language) DO UPDATE SET draft_document_id=$3,version=branch_menus.version+1,updated_at=now()`, branch, brand, document, language)
	if err != nil {
		return state, err
	}
	if state.Draft != nil {
		if err = retireMenuDocument(ctx, tx, state.Draft.ID); err != nil {
			return state, err
		}
	}
	return finishMenu(ctx, tx, brand, branch, language, sign)
}
func (r *Repository) ChangeMenuPublication(ctx context.Context, actor, brand, branch int64, language model.MenuLanguage, version int, action, document string, sign MenuStateSigner) (model.BranchMenuState, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.BranchMenuState{}, err
	}
	defer tx.Rollback(ctx)
	if err = r.lockMenu(ctx, tx, actor, brand, branch, true); err != nil {
		return model.BranchMenuState{}, err
	}
	state, err := readMenuState(ctx, tx, brand, branch, language)
	if err != nil {
		return state, err
	}
	if state.Version != version {
		return state, ErrPreconditionFailed
	}
	var retired *model.MenuDocument
	switch action {
	case "publish":
		if state.Published != nil && state.Published.ID == document {
			return finishMenu(ctx, tx, brand, branch, language, sign)
		}
		if state.Draft == nil || state.Draft.ID != document {
			return state, ErrNotFound
		}
		retired = state.Published
		_, err = tx.Exec(ctx, `UPDATE branch_menus SET published_document_id=draft_document_id,draft_document_id=NULL,version=version+1,updated_at=now() WHERE sucursal_id=$1 AND language=$2`, branch, language)
	case "unpublish":
		if state.Published == nil {
			return finishMenu(ctx, tx, brand, branch, language, sign)
		}
		retired = state.Published
		_, err = tx.Exec(ctx, `UPDATE branch_menus SET published_document_id=NULL,version=version+1,updated_at=now() WHERE sucursal_id=$1 AND language=$2`, branch, language)
	case "discard":
		if state.Draft == nil {
			return finishMenu(ctx, tx, brand, branch, language, sign)
		}
		retired = state.Draft
		_, err = tx.Exec(ctx, `UPDATE branch_menus SET draft_document_id=NULL,version=version+1,updated_at=now() WHERE sucursal_id=$1 AND language=$2`, branch, language)
	default:
		return state, ErrInvalidRequest
	}
	if err != nil {
		return state, err
	}
	if retired != nil {
		if err = retireMenuDocument(ctx, tx, retired.ID); err != nil {
			return state, err
		}
	}
	return finishMenu(ctx, tx, brand, branch, language, sign)
}

// Only detached pending uploads can be compensated. PostgreSQL rechecks the
// pending predicate after waiting on an activation, protecting ambiguous COMMITs.
func (r *Repository) FailMenuUpload(ctx context.Context, id string) error {
	_, err := r.Pool.Exec(ctx, `UPDATE branch_menu_documents d SET estado='DELETE_PENDING',delete_after=clock_timestamp()+interval '6 minutes',lease_owner=NULL,lease_until=NULL WHERE id=$1 AND estado='UPLOAD_PENDING' AND NOT EXISTS(SELECT 1 FROM branch_menus m WHERE m.draft_document_id=d.id OR m.published_document_id=d.id)`, id)
	return err
}

func (r *Repository) PublicBranchMenu(ctx context.Context, branch int64, language model.MenuLanguage, sign func(context.Context, *model.MenuDocument) error) (model.PublicBranchMenu, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.PublicBranchMenu{}, err
	}
	defer tx.Rollback(ctx)
	var out model.PublicBranchMenu
	// Resolve identity first, then lock brand -> branch -> menu. A withdrawal
	// cannot retire the object until this request has finished signing its URL.
	err = tx.QueryRow(ctx, `SELECT marca_id FROM sucursales WHERE id=$1`, branch).Scan(&out.BrandID)
	if err != nil {
		return out, menuNotFound(err)
	}
	err = tx.QueryRow(ctx, `SELECT nombre FROM marcas WHERE id=$1 AND activo AND deleted_at IS NULL FOR SHARE`, out.BrandID).Scan(&out.BrandName)
	if err != nil {
		return out, menuNotFound(err)
	}
	err = tx.QueryRow(ctx, `SELECT id,nombre FROM sucursales WHERE id=$1 AND marca_id=$2 AND activo AND deleted_at IS NULL FOR SHARE`, branch, out.BrandID).Scan(&out.BranchID, &out.BranchName)
	if err != nil {
		return out, menuNotFound(err)
	}
	var published *string
	err = tx.QueryRow(ctx, `SELECT published_document_id::text FROM branch_menus WHERE sucursal_id=$1 AND marca_id=$2 AND language=$3 FOR SHARE`, branch, out.BrandID, language).Scan(&published)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if published != nil {
		out.Published, err = readMenuDocument(ctx, tx, *published)
		if err != nil {
			return out, err
		}
		if sign != nil {
			if err = sign(ctx, out.Published); err != nil {
				return model.PublicBranchMenu{}, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return model.PublicBranchMenu{}, err
	}
	return out, nil
}
func (r *Repository) PublicBrandMenus(ctx context.Context, brand int64) ([]model.PublicBrandMenu, error) {
	var name string
	err := r.Pool.QueryRow(ctx, `SELECT nombre FROM marcas WHERE id=$1 AND activo AND deleted_at IS NULL`, brand).Scan(&name)
	if err != nil {
		return nil, menuNotFound(err)
	}
	rows, err := r.Pool.Query(ctx, `SELECT s.id,s.nombre,s.direccion,CASE WHEN s.latitud BETWEEN -90 AND 90 AND s.longitud BETWEEN -180 AND 180 THEN s.latitud END,CASE WHEN s.latitud BETWEEN -90 AND 90 AND s.longitud BETWEEN -180 AND 180 THEN s.longitud END,EXISTS(SELECT 1 FROM branch_menus b JOIN branch_menu_documents d ON d.id=b.published_document_id AND d.estado='READY' WHERE b.sucursal_id=s.id AND b.language='es') FROM sucursales s JOIN marcas m ON m.id=s.marca_id AND m.activo AND m.deleted_at IS NULL WHERE s.marca_id=$1 AND s.activo AND s.deleted_at IS NULL ORDER BY lower(s.nombre),s.id`, brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.PublicBrandMenu{}
	for rows.Next() {
		x := model.PublicBrandMenu{BrandID: brand, BrandName: name}
		if err = rows.Scan(&x.BranchID, &x.BranchName, &x.Address, &x.Latitude, &x.Longitude, &x.HasMenu); err != nil {
			return nil, err
		}
		items = append(items, x)
	}
	return items, rows.Err()
}

func (r *Repository) DueMenuMedia(ctx context.Context, limit int, owner string) ([]MediaDeletion, error) {
	rows, err := r.Pool.Query(ctx, `UPDATE branch_menu_documents d SET estado='DELETE_PENDING',lease_owner=$2,lease_until=now()+interval '5 minutes' WHERE id IN(SELECT id FROM branch_menu_documents d WHERE (lease_until IS NULL OR lease_until<now()) AND ((estado='DELETE_PENDING' AND delete_after<=now()) OR (estado='UPLOAD_PENDING' AND created_at<now()-interval '15 minutes')) AND NOT EXISTS(SELECT 1 FROM branch_menus m WHERE m.draft_document_id=d.id OR m.published_document_id=d.id) ORDER BY COALESCE(delete_after,created_at),id LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING id::text,object_key,estado`, limit, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MediaDeletion{}
	for rows.Next() {
		var d MediaDeletion
		if err = rows.Scan(&d.ID, &d.ObjectKey, &d.Status); err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return items, rows.Err()
}
func (r *Repository) CompleteMenuMediaDeletion(ctx context.Context, id, owner string, failed bool) error {
	q := `UPDATE branch_menu_documents SET estado='DELETED',object_key='deleted/menu/'||id::text,lease_owner=NULL,lease_until=NULL,delete_after=NULL WHERE id=$1 AND lease_owner=$2 AND estado='DELETE_PENDING'`
	if failed {
		q = `UPDATE branch_menu_documents SET lease_owner=NULL,lease_until=NULL,delete_after=now()+interval '1 hour' WHERE id=$1 AND lease_owner=$2 AND estado='DELETE_PENDING'`
	}
	tag, err := r.Pool.Exec(ctx, q, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
