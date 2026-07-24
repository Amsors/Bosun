package monitor

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type OwnerStore interface {
	ListAgentOwners(context.Context) (map[string]AgentOwner, error)
}

type PgxOwnerStore struct {
	pool *pgxpool.Pool
}

func NewPgxOwnerStore(pool *pgxpool.Pool) *PgxOwnerStore {
	return &PgxOwnerStore{pool: pool}
}

func (s *PgxOwnerStore) ListAgentOwners(ctx context.Context) (map[string]AgentOwner, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sessions.id::text, users.email, sessions.display_name
		FROM bosun.sessions AS sessions
		JOIN bosun.users AS users ON users.id = sessions.user_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list agent Pod owners: %w", err)
	}
	defer rows.Close()

	owners := make(map[string]AgentOwner)
	for rows.Next() {
		var sessionID string
		var owner AgentOwner
		if err := rows.Scan(&sessionID, &owner.Username, &owner.SessionName); err != nil {
			return nil, fmt.Errorf("scan agent Pod owner: %w", err)
		}
		owners[sessionID] = owner
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent Pod owners: %w", err)
	}
	return owners, nil
}
