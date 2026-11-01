package repository

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	Pool                    *pgxpool.Pool
	Now                     func() time.Time
	OutboxCipherKey         []byte
	AppleBillingEnvironment string
}

func New(pool *pgxpool.Pool, outboxCipherKey ...[]byte) *Repository {
	repository := &Repository{Pool: pool, Now: time.Now}
	if len(outboxCipherKey) > 0 {
		repository.OutboxCipherKey = append([]byte(nil), outboxCipherKey[0]...)
	}
	return repository
}
