package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	db "github.com/Amsors/Bosun/backend/internal/database/sqlc"
)

type sessionQuerier interface {
	GetGatewaySessionIdentity(context.Context, db.GetGatewaySessionIdentityParams) (db.GetGatewaySessionIdentityRow, error)
}

type DatabaseSessionResolver struct {
	queries sessionQuerier
	timeout time.Duration
}

func NewDatabaseSessionResolver(queries sessionQuerier, timeout time.Duration) *DatabaseSessionResolver {
	return &DatabaseSessionResolver{queries: queries, timeout: timeout}
}

func (r *DatabaseSessionResolver) Resolve(ctx context.Context, namespace, crName string) (SessionIdentity, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	row, err := r.queries.GetGatewaySessionIdentity(lookupCtx, db.GetGatewaySessionIdentityParams{
		CrNamespace: namespace,
		CrName:      crName,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionIdentity{}, ErrSessionUnavailable
	}
	if err != nil {
		return SessionIdentity{}, fmt.Errorf("query session identity: %w", err)
	}
	return SessionIdentity{
		SessionID:    row.ID.String(),
		Namespace:    row.CrNamespace,
		CRName:       row.CrName,
		Phase:        row.Phase,
		ProviderMode: row.ProviderMode,
	}, nil
}
