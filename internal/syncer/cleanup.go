package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
)

// prune is called only after a successful full reconciliation. It considers only
// GitHub teams owned by the managed parent, never unrelated organization teams.
func (s *Syncer) prune(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	slugs, err := s.Reconciler.ManagedTeamSlugs(ctx)
	if err != nil {
		return fmt.Errorf("list managed GitHub teams for cleanup: %w", err)
	}
	// Re-read Nais after reconciliation so newly added teams are protected before
	// any orphan deletion starts. Nais and GitHub use the same team slug.
	teams, err := s.teams(ctx)
	if err != nil {
		return err
	}
	desired := make(map[string]bool)
	for _, team := range teams {
		desired[strings.ToLower(team.Slug)] = true
	}
	for _, slug := range slugs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if desired[strings.ToLower(slug)] {
			continue
		}
		if err := s.deleteOrphan(ctx, slug); err != nil {
			return fmt.Errorf("delete orphaned GitHub team %q: %w", slug, err)
		}
	}
	return ctx.Err()
}

func (s *Syncer) deleteOrphan(ctx context.Context, slug string) error {
	ctx, cancel := context.WithTimeout(ctx, s.TeamTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	log := s.Log.With("github_team", slug)
	err := s.Reconciler.Delete(ctx, s.Client, &naisapi.Team{Slug: slug}, log)
	return errors.Join(err, ctx.Err())
}
