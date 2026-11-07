package repository

import "context"

func (r *Repository) RecordAppOpening(ctx context.Context, userID int64, authVersion int) error {
	_, err := r.Pool.Exec(ctx, `UPDATE usuarios
		SET last_app_opened_at=GREATEST(last_app_opened_at, statement_timestamp())
		WHERE id=$1 AND auth_version=$2 AND activo AND deleted_at IS NULL`, userID, authVersion)
	return err
}
