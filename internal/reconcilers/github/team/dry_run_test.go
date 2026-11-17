package github_team_reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/nais/nais-github-teamsync/internal/syncer"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/mock"
)

type previewSource struct {
	teams   []*naisapi.Team
	members []*naisapi.Member
	err     error
}

func (s *previewSource) Teams(context.Context) ([]*naisapi.Team, error) { return s.teams, s.err }
func (s *previewSource) Team(_ context.Context, slug string) (*naisapi.Team, error) {
	for _, team := range s.teams {
		if team.Slug == slug {
			return team, s.err
		}
	}
	return nil, s.err
}

func (s *previewSource) Members(context.Context, string) ([]*naisapi.Member, error) {
	return s.members, s.err
}

func previewLog() (*slog.Logger, *bytes.Buffer) {
	var output bytes.Buffer
	return slog.New(slog.NewJSONHandler(&output, nil)), &output
}

func checkPreviewOperations(t *testing.T, output *bytes.Buffer, want []string) []map[string]any {
	t.Helper()
	var got []string
	var records []map[string]any
	decoder := json.NewDecoder(output)
	for {
		var record map[string]any
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if record["dry_run"] != true || record["organization"] != "org" {
			t.Errorf("missing dry-run context: %v", record)
		}
		operation, ok := record["operation"].(string)
		if !ok {
			t.Fatalf("missing operation: %v", record)
		}
		got = append(got, operation)
		records = append(records, record)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("operations mismatch (-want +got):\n%s", diff)
	}
	return records
}

func expectPreviewIdentity(graph *MockGraphClient, times int) {
	graph.EXPECT().Query(mock.Anything, mock.Anything, map[string]any{"org": githubv4.String("org"), "username": githubv4.String("new@example.com")}).
		Run(func(_ context.Context, q any, _ map[string]any) {
			query := q.(*LookupGitHubSamlUserByEmail)
			query.Organization.SamlIdentityProvider = &ExternalIdentityProvider{ExternalIdentities: &ExternalIdentityConnection{}}
			query.Organization.SamlIdentityProvider.ExternalIdentities.Nodes = []ExternalIdentity{{User: GitHubUser{Login: "new-user"}}}
		}).Return(nil).Times(times)
}

func TestDryRunExistingTeam(t *testing.T) {
	ctx := t.Context()
	teams := NewMockTeamsService(t)
	graph := NewMockGraphClient(t)
	parent := &github.Team{ID: new(int64(123)), Slug: new(parentTeamSlug)}
	teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).Return(parent, previewResponse(http.StatusOK), nil).Once()
	teams.EXPECT().GetTeamBySlug(ctx, "org", "child").Return(&github.Team{
		Slug: new("child"), Description: new("old purpose"), Privacy: new("closed"), Parent: parent,
	}, previewResponse(http.StatusOK), nil).Once()
	expectDirectMembers(t, graph, "child", "old-user")
	expectPreviewIdentity(graph, 1)
	r := New("org", teams, graph, WithDryRun(true))
	log, output := previewLog()
	if err := r.Reconcile(ctx, &previewSource{members: []*naisapi.Member{{Email: "new@example.com"}}}, &naisapi.Team{Slug: "child", Purpose: "new purpose"}, log); err != nil {
		t.Fatal(err)
	}
	records := checkPreviewOperations(t, output, []string{"update_team", "remove_team_member", "add_team_member"})
	if len(records) != 3 {
		t.Fatal("expected three write previews")
	}
	if records[0]["description"] != "new purpose" || records[0]["parent_team"] != parentTeamSlug {
		t.Errorf("missing team update details: %v", records[0])
	}
	if records[1]["username"] != "old-user" || records[2]["username"] != "new-user" {
		t.Errorf("missing membership details: %v", records)
	}
}

func TestDryRunMissingTeamsUsesFreshPlanEachTime(t *testing.T) {
	ctx := t.Context()
	teams := NewMockTeamsService(t)
	graph := NewMockGraphClient(t)
	teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).Return(nil, previewResponse(http.StatusNotFound), nil).Twice()
	teams.EXPECT().GetTeamBySlug(ctx, "org", "child").Return(nil, previewResponse(http.StatusNotFound), nil).Twice()
	expectPreviewIdentity(graph, 2)
	r := New("org", teams, graph, WithDryRun(true))
	for range 2 {
		log, output := previewLog()
		if err := r.Reconcile(ctx, &previewSource{members: []*naisapi.Member{{Email: "new@example.com"}}}, &naisapi.Team{Slug: "child", Purpose: "purpose"}, log); err != nil {
			t.Fatal(err)
		}
		checkPreviewOperations(t, output, []string{"create_team", "create_team", "add_team_member"})
	}
}

func TestDryRunDeletionAndOwnership(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrelated", true: "owned"}[owned], func(t *testing.T) {
			ctx := t.Context()
			teams := NewMockTeamsService(t)
			graph := NewMockGraphClient(t)
			parentID := int64(999)
			if owned {
				parentID = 123
			}
			teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
				Return(&github.Team{ID: new(int64(123))}, previewResponse(http.StatusOK), nil).Once()
			teams.EXPECT().GetTeamBySlug(ctx, "org", "child").Return(&github.Team{
				Slug: new("child"), Parent: &github.Team{ID: &parentID},
			}, previewResponse(http.StatusOK), nil).Once()
			r := New("org", teams, graph, WithDryRun(true))
			log, output := previewLog()
			err := r.Delete(ctx, nil, &naisapi.Team{Slug: "child"}, log)
			if owned {
				if err != nil {
					t.Fatal(err)
				}
				checkPreviewOperations(t, output, []string{"delete_team"})
			} else {
				if err == nil {
					t.Fatal("expected ownership safeguard error")
				}
				if output.Len() != 0 {
					t.Errorf("unrelated team must not be scheduled for deletion: %s", output)
				}
			}
		})
	}
}

func TestDryRunMissingParentCannotAdoptExistingTeam(t *testing.T) {
	ctx := t.Context()
	teams := NewMockTeamsService(t)
	graph := NewMockGraphClient(t)
	child := &github.Team{Slug: new("child"), Parent: &github.Team{ID: new(int64(math.MaxInt64))}}
	teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).Return(nil, previewResponse(http.StatusNotFound), nil).Once()
	teams.EXPECT().GetTeamBySlug(ctx, "org", "child").Return(child, previewResponse(http.StatusOK), nil).Once()
	r := New("org", teams, graph, WithDryRun(true))
	log, output := previewLog()
	if err := r.Reconcile(ctx, nil, &naisapi.Team{Slug: "child"}, log); err == nil {
		t.Fatal("expected ownership safeguard error")
	}
	checkPreviewOperations(t, output, []string{"create_team"})
	if child.GetParent().GetID() != math.MaxInt64 {
		t.Error("preview must not modify the remote team snapshot")
	}
}

func TestDryRunInaccessibleSAMLPreventsMembershipPlan(t *testing.T) {
	ctx := t.Context()
	teams := NewMockTeamsService(t)
	graph := NewMockGraphClient(t)
	parent := &github.Team{ID: new(int64(123))}
	teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).Return(parent, previewResponse(http.StatusOK), nil).Once()
	teams.EXPECT().GetTeamBySlug(ctx, "org", "child").Return(&github.Team{
		Slug: new("child"), Privacy: new("closed"), Parent: parent,
	}, previewResponse(http.StatusOK), nil).Once()
	expectDirectMembers(t, graph, "child", "old-user")
	graph.EXPECT().Query(ctx, mock.Anything, mock.Anything).Return(nil).Once()
	r := New("org", teams, graph, WithDryRun(true))
	log, output := previewLog()
	if err := r.Reconcile(ctx, &previewSource{members: []*naisapi.Member{{Email: "new@example.com"}}}, &naisapi.Team{Slug: "child"}, log); err == nil {
		t.Fatal("expected SAML safeguard error")
	}
	checkPreviewOperations(t, output, nil)
}

func TestDryRunPreviewsOrphanCleanup(t *testing.T) {
	ctx := t.Context()
	teams := NewMockTeamsService(t)
	graph := NewMockGraphClient(t)
	parent := &github.Team{ID: new(int64(123)), Slug: new(parentTeamSlug)}
	teams.EXPECT().GetTeamBySlug(mock.Anything, "org", parentTeamSlug).Return(parent, previewResponse(http.StatusOK), nil).Twice()
	teams.EXPECT().ListChildTeamsByParentSlug(mock.Anything, "org", parentTeamSlug, mock.Anything).
		Return([]*github.Team{{Slug: new("orphan"), Parent: parent}}, previewResponse(http.StatusOK), nil).Once()
	teams.EXPECT().GetTeamBySlug(mock.Anything, "org", "orphan").
		Return(&github.Team{Slug: new("orphan"), Parent: parent}, previewResponse(http.StatusOK), nil).Once()
	r := New("org", teams, graph, WithDryRun(true))
	log, output := previewLog()
	s := &syncer.Syncer{Client: &previewSource{}, Reconciler: r, Log: log, Workers: 1, Timeout: time.Minute, TeamTimeout: time.Second}
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	// The final sync-complete log is not a write preview.
	decoder := json.NewDecoder(output)
	var write map[string]any
	if err := decoder.Decode(&write); err != nil {
		t.Fatal(err)
	}
	if write["operation"] != "delete_team" || write["github_team"] != "orphan" || write["dry_run"] != true {
		t.Errorf("missing orphan deletion preview: %v", write)
	}
}

func TestOrphanCleanupRechecksOwnershipBeforeDeletion(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "live writes", true: "dry run"}[dryRun], func(t *testing.T) {
			teams := NewMockTeamsService(t)
			graph := NewMockGraphClient(t)
			parent := &github.Team{ID: new(int64(123)), Slug: new(parentTeamSlug)}
			teams.EXPECT().GetTeamBySlug(mock.Anything, "org", parentTeamSlug).
				Return(parent, previewResponse(http.StatusOK), nil).Twice()
			teams.EXPECT().ListChildTeamsByParentSlug(mock.Anything, "org", parentTeamSlug, mock.Anything).
				Return([]*github.Team{{Slug: new("orphan"), Parent: parent}}, previewResponse(http.StatusOK), nil).Once()
			// The team was moved out of our hierarchy after the inventory read.
			// Neither live nor dry-run mode may proceed with its deletion.
			teams.EXPECT().GetTeamBySlug(mock.Anything, "org", "orphan").
				Return(&github.Team{Slug: new("orphan"), Parent: &github.Team{ID: new(int64(999))}}, previewResponse(http.StatusOK), nil).Once()
			r := New("org", teams, graph, WithDryRun(dryRun))
			log, output := previewLog()
			s := &syncer.Syncer{Client: &previewSource{}, Reconciler: r, Log: log, Workers: 1, Timeout: time.Minute, TeamTimeout: time.Second}
			if err := s.Sync(t.Context()); err == nil {
				t.Fatal("expected ownership safeguard error")
			}
			if output.Len() != 0 {
				t.Errorf("unowned orphan must not be previewed for deletion: %s", output)
			}
		})
	}
}

func TestDryRunProtectsReservedParent(t *testing.T) {
	teams := NewMockTeamsService(t)
	graph := NewMockGraphClient(t)
	r := New("org", teams, graph, WithDryRun(true))
	log, output := previewLog()
	if err := r.Delete(t.Context(), nil, &naisapi.Team{Slug: parentTeamSlug}, log); err == nil {
		t.Fatal("expected reserved-parent safeguard error")
	}
	if output.Len() != 0 {
		t.Errorf("reserved parent must not be scheduled for deletion: %s", output)
	}
}

func TestDryRunCanceledWritesAreNotLogged(t *testing.T) {
	log, output := previewLog()
	s := &previewTeamsService{base: NewMockTeamsService(t), log: log, created: make(map[string]*github.Team)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.DeleteTeamBySlug(ctx, "org", "child"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if output.Len() != 0 {
		t.Errorf("canceled operation must not be logged as planned: %s", output)
	}
}
