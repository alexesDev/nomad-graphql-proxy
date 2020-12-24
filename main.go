package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/graphql-go/graphql"
	"github.com/hashicorp/nomad/api"
)

type postData struct {
	Query     string                 `json:"query"`
	Operation string                 `json:"operation"`
	Variables map[string]interface{} `json:"variables"`
}

func main() {
	log.SetFlags(0)

	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		log.Fatal("failed to create nomad client:", err)
	}

	schema, err := buildSchema(client)
	if err != nil {
		log.Fatal("failed to create new schema:", err)
	}

	http.HandleFunc("/graphql", func(w http.ResponseWriter, req *http.Request) {
		var p postData
		if err := json.NewDecoder(req.Body).Decode(&p); err != nil {
			w.WriteHeader(400)
			return
		}
		result := graphql.Do(graphql.Params{
			Context:        req.Context(),
			Schema:         schema,
			RequestString:  p.Query,
			VariableValues: p.Variables,
			OperationName:  p.Operation,
		})
		if err := json.NewEncoder(w).Encode(result); err != nil {
			log.Println("could not write result to response:", err)
		}
	})

	if os.Getenv("PLAYGROUND") != "disabled" {
		http.Handle("/", playground.Handler("Nomad", "/graphql"))
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":5577"
	}

	log.Println("Start listening", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
