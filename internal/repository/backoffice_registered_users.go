package repository

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Registered users are product accounts, never the separate staff identities.
// Deleted accounts are excluded; no authentication material is selected.
type BackofficeRegisteredUser struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	LastName        *string    `json:"last_name"`
	Email           string     `json:"email"`
	AccountType     string     `json:"account_type"`
	Active          bool       `json:"active"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type BackofficeRegisteredUsersPage struct {
	Items      []BackofficeRegisteredUser `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
}

func (r *Repository) ListBackofficeRegisteredUsers(ctx context.Context, query, accountType string, limit int, before int64) (BackofficeRegisteredUsersPage, error) {
	out := BackofficeRegisteredUsersPage{Items: make([]BackofficeRegisteredUser, 0)}
	query = strings.TrimSpace(query)
	if limit < 1 || limit > 100 || before < 0 || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 120 || (accountType != "" && accountType != "CLIENTE_FINAL" && accountType != "PERSONAL_MARCA") {
		return out, ErrInvalidRequest
	}
	rows, err := r.Pool.Query(ctx, `SELECT id,nombre,apellido,email::text,tipo_cuenta,activo,email_verified_at,created_at
 FROM usuarios WHERE deleted_at IS NULL
 AND ($1='' OR strpos(lower(concat_ws(' ',nombre,apellido,email::text)),lower($1))>0)
 AND ($2='' OR tipo_cuenta=$2) AND ($3::bigint=0 OR id<$3)
 ORDER BY id DESC LIMIT $4`, query, accountType, before, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var user BackofficeRegisteredUser
		if err := rows.Scan(&user.ID, &user.Name, &user.LastName, &user.Email, &user.AccountType, &user.Active, &user.EmailVerifiedAt, &user.CreatedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, user)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		cursor := strconv.FormatInt(out.Items[limit-1].ID, 10)
		out.NextCursor = &cursor
		out.Items = out.Items[:limit]
	}
	return out, nil
}
