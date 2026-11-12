package naisapi

import (
	_ "embed"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
)

//go:embed schema.graphql
var schemaSource string

//go:embed queries.graphql
var querySource string

func TestQueriesMatchSchema(t *testing.T) {
	// The CLI's introspection output already declares built-in scalars. Parse
	// and validate it directly instead of adding gqlparser's duplicate prelude.
	document, err := parser.ParseSchema(&ast.Source{Name: "schema.graphql", Input: schemaSource})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := validator.ValidateSchemaDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	queries, errs := gqlparser.LoadQueryWithRules(schema, querySource, nil)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(queries.Operations) != 3 {
		t.Errorf("operations = %d, want 3", len(queries.Operations))
	}
	for _, operation := range queries.Operations {
		if operation.Operation != ast.Query {
			t.Errorf("Nais API operation %q must be read-only, got %s", operation.Name, operation.Operation)
		}
	}
}
