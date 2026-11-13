package naisapi

// Team is the desired state read from the Nais GraphQL API. Its slug identifies
// the team in both Nais and GitHub; a matching slug alone does not prove ownership.
type Team struct {
	Slug               string
	Purpose            string
	DeletionInProgress bool
}

// Member contains the identity needed to resolve a Nais member in GitHub SSO.
type Member struct {
	Email string
}
