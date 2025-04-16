package graph

//go:generate go run github.com/99designs/gqlgen generate
import "gqlfed/instances/graph/model"

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require here.

type Resolver struct {
	instances []*model.Instance
}
