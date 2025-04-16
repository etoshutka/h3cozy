package main

import (
	"gqlfed/instances/graph"
	"log"
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/go-chi/chi"
	"github.com/rs/cors"
)

const defaultPort = "4001"

func main() {
	port := defaultPort

	router := chi.NewRouter()
	// Add CORS middleware around every request
	// See https://github.com/rs/cors for full option listing
	router.Use(cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
		Debug:            true,
	}).Handler)

	srv := handler.New(graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{}}))

	srv.AddTransport(transport.Websocket{
		// Keep-alives are important for WebSockets to detect dead connections. This is
		// not unlike asking a partner who seems to have zoned out while you tell them
		// a story crucial to understanding the dynamics of your workplace: "Are you
		// listening to me?"
		//
		// Failing to set a keep-alive interval can result in the connection being held
		// open and the server expending resources to communicate with a client that has
		// long since walked to the kitchen to make a sandwich instead.
		KeepAlivePingInterval: 10 * time.Second,

		// The `github.com/gorilla/websocket.Upgrader` is used to handle the transition
		// from an HTTP connection to a WebSocket connection. Among other options, here
		// you must check the origin of the request to prevent cross-site request forgery
		// attacks.
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	})

	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})

	srv.SetQueryCache(lru.New[*ast.QueryDocument](1000))

	srv.Use(extension.Introspection{})
	srv.Use(extension.AutomaticPersistedQuery{
		Cache: lru.New[string](100),
	})

	router.Handle("/", playground.Handler("GraphQL playground", "/query"))
	router.Handle("/query", srv)

	log.Printf("connect to http://localhost:%s/ for GraphQL playground", port)
	log.Fatal(http.ListenAndServeTLS(":"+port, "cert.pem", "key.pem", router))
}
