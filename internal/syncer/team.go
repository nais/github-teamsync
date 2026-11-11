package syncer

import (
	"context"
	"errors"
	"fmt"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
)

// SyncTeam reads fresh desired state for one queued slug. Its single timeout
// covers both the lookup and GitHub reconciliation. A missing team is an error,
// never a deletion instruction; targeted work never performs orphan cleanup.
func (s *Syncer) SyncTeam(ctx context.Context, slug string) error {
	ctx, cancel := context.WithTimeout(ctx, s.TeamTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if slug == "" {
		return fmt.Errorf("cannot sync an empty Nais team slug")
	}
	team, err := s.Client.Team(ctx, slug)
	if err != nil {
		return fmt.Errorf("fetch Nais team %q: %w", slug, err)
	}
	if team == nil || team.Slug != slug {
		return fmt.Errorf("nais API returned incomplete or mismatched team state for %q", slug)
	}
	if err := s.reconcileTeam(ctx, team); err != nil {
		return fmt.Errorf("sync Nais team %q: %w", slug, err)
	}
	s.Log.InfoContext(ctx, "targeted GitHub team sync completed", "team", slug)
	return nil
}

// reconcileTeam applies only Nais state, never webhook payloads. The caller owns
// the deadline so a targeted lookup does not get a fresh timeout for its writes.
func (s *Syncer) reconcileTeam(ctx context.Context, team *naisapi.Team) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	log := s.Log.With("team", team.Slug)
	var err error
	if team.DeletionInProgress {
		err = s.Reconciler.Delete(ctx, s.Client, team, log)
	} else {
		err = s.Reconciler.Reconcile(ctx, s.Client, team, log)
	}
	// An operation completing after its deadline must not count as a success or
	// allow a full cycle to proceed to cleanup, even if it returned no error.
	return errors.Join(err, ctx.Err())
}
