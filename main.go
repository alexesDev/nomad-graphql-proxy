package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"
	"github.com/hashicorp/nomad/api"
)

type postData struct {
	Query     string                 `json:"query"`
	Operation string                 `json:"operation"`
	Variables map[string]interface{} `json:"variables"`
}

var typeMap map[reflect.Kind]*graphql.Scalar

func init() {
	typeMap = map[reflect.Kind]*graphql.Scalar{
		reflect.String:  graphql.String,
		reflect.Bool:    graphql.Boolean,
		reflect.Int:     graphql.Int,
		reflect.Int8:    graphql.Int,
		reflect.Int16:   graphql.Int,
		reflect.Int32:   graphql.Int,
		reflect.Int64:   graphql.Int,
		reflect.Float32: graphql.Float,
		reflect.Float64: graphql.Float,
		reflect.Uint8:   graphql.Int,
		reflect.Uint32:  graphql.Int,
		reflect.Uint64:  graphql.Int,
	}
}

func convert(objectType reflect.Type, registry *map[string]graphql.Output) graphql.Output {
	existType, ok := (*registry)[objectType.Name()]
	if ok {
		return existType
	}

	allocFields := graphql.Fields{}

	for i := 0; i < objectType.NumField(); i++ {
		currentField := objectType.Field(i)

		// map
		if currentField.Type.Kind() == reflect.Map {
			valType := currentField.Type.Elem()
			var valGraphql graphql.Output

			gt, ok := typeMap[valType.Kind()]
			if ok { // of scalars
				valGraphql = gt
			} else {
				if valType.Kind() == reflect.Ptr { // of pointers
					valType = valType.Elem()
				}

				valGraphql = convert(valType, registry) // of structs
			}

			itemType := graphql.NewObject(
				graphql.ObjectConfig{
					Name: currentField.Name + "MapItem",
					Fields: graphql.Fields{
						"key": &graphql.Field{
							Type: graphql.NewNonNull(graphql.String),
						},
						"value": &graphql.Field{
							Type: graphql.NewNonNull(valGraphql),
						},
					},
				},
			)

			allocFields[currentField.Name] = &graphql.Field{
				Name: currentField.Name,
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(itemType))),
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					parent := reflect.ValueOf(p.Source).Elem()
					res := []map[string]interface{}{}
					iter := parent.FieldByName(currentField.Name).MapRange()
					for iter.Next() {
						res = append(res, map[string]interface{}{
							"key":   iter.Key().String(),
							"value": iter.Value().Interface(),
						})
					}

					return res, nil
				},
			}

			continue
		}

		if currentField.Type.Kind() == reflect.Struct {
			if currentField.Type.Name() == "Time" { // time.Time
				allocFields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(graphql.Int),
					Resolve: func(p graphql.ResolveParams) (interface{}, error) {
						parent := reflect.ValueOf(p.Source).Elem()
						val := parent.FieldByName(currentField.Name).Interface().(time.Time)
						return val.Unix(), nil
					},
				}
			} else { // user-defined struct
				allocFields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(convert(currentField.Type, registry)),
				}
			}

			continue
		}

		// pointer to struct
		if currentField.Type.Kind() == reflect.Ptr && currentField.Type.Elem().Kind() == reflect.Struct {
			allocFields[currentField.Name] = &graphql.Field{
				Name: currentField.Name,
				Type: convert(currentField.Type.Elem(), registry),
			}

			continue
		}

		// slice
		if currentField.Type.Kind() == reflect.Slice {
			if currentField.Type.Elem().Kind() == reflect.Struct { // of struct
				allocFields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(convert(currentField.Type.Elem(), registry)))),
				}
			} else if currentField.Type.Elem().Kind() == reflect.Ptr { // of pointer
				allocFields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(graphql.NewList(convert(currentField.Type.Elem().Elem(), registry))),
				}
			} else { // of scalar type
				gt, ok := typeMap[currentField.Type.Elem().Kind()]
				if ok {
					allocFields[currentField.Name] = &graphql.Field{
						Name: currentField.Name,
						Type: graphql.NewNonNull(graphql.NewList(gt)),
					}
				}
			}

			continue
		}

		// pointer to scalar type
		if currentField.Type.Kind() == reflect.Ptr {
			graphqlType, ok := typeMap[currentField.Type.Elem().Kind()]
			if ok {
				allocFields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphqlType,
				}
				continue
			}
		}

		graphqlType, ok := typeMap[currentField.Type.Kind()]
		if ok {
			allocFields[currentField.Name] = &graphql.Field{
				Name: currentField.Name,
				Type: graphql.NewNonNull(graphqlType),
			}
		} else {
			log.Println("  skip", objectType.Name(), currentField.Name, currentField.Type.Kind())
		}
	}

	graphqlType := graphql.NewObject(
		graphql.ObjectConfig{
			Name:   objectType.Name(),
			Fields: allocFields,
		},
	)

	(*registry)[objectType.Name()] = graphqlType

	return graphqlType
}

func main() {
	log.SetFlags(0)

	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		panic(err)
	}

	graphqlRegistry := map[string]graphql.Output{}
	allocType := convert(reflect.TypeOf(api.AllocationListStub{}), &graphqlRegistry)

	fields := graphql.Fields{
		"allocations": &graphql.Field{
			Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(allocType))),
			Args: graphql.FieldConfigArgument{
				"prefix": &graphql.ArgumentConfig{
					Type: graphql.String,
				},
				"namespace": &graphql.ArgumentConfig{
					Type: graphql.String,
				},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				params := map[string]string{
					"task_states": "false",
				}

				for key, value := range p.Args {
					params[key] = value.(string)
				}

				for _, s := range p.Info.FieldASTs[0].SelectionSet.Selections {
					field := s.(*ast.Field)

					switch field.Name.Value {
					case "AllocatedResources":
						params["resources"] = "true"
					case "TaskStates":
						params["task_states"] = "true"
					}
				}

				allocs, _, err := client.Allocations().List(&api.QueryOptions{
					Params: params,
				})

				return allocs, err
			},
		},
	}
	rootQuery := graphql.ObjectConfig{Name: "Query", Fields: fields}
	schemaConfig := graphql.SchemaConfig{Query: graphql.NewObject(rootQuery)}
	schema, err := graphql.NewSchema(schemaConfig)
	if err != nil {
		log.Fatalf("failed to create new schema, error: %v", err)
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
			fmt.Printf("could not write result to response: %s", err)
		}
	})

	http.ListenAndServe(":5577", nil)
}
