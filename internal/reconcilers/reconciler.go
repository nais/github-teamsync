package reconcilers

import (
	"context"
	"log/slog"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
)

type Reconciler interface {
	ManagedTeamSlugs(ctx context.Context) ([]string, error)
	Reconcile(ctx context.Context, source TeamSource, naisTeam *naisapi.Team, log *slog.Logger) error
	Delete(ctx context.Context, source TeamSource, naisTeam *naisapi.Team, log *slog.Logger) error
}

type TeamSource interface {
	Members(ctx context.Context, slug string) ([]*naisapi.Member, error)
}
