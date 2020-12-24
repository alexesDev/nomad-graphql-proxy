package main

import (
	"log"
	"reflect"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"

	"github.com/hashicorp/nomad/api"
)

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

func reflectTransform(objectType reflect.Type, registry *map[string]graphql.Output) graphql.Output {
	existType, ok := (*registry)[objectType.Name()]
	if ok {
		return existType
	}

	fields := graphql.Fields{}

	for i := 0; i < objectType.NumField(); i++ {
		currentField := objectType.Field(i)

		// map
		if currentField.Type.Kind() == reflect.Map {
			valType := currentField.Type.Elem()

			// TODO: fix this
			// skip Config map[string]interface{}
			if valType.String() == "interface {}" {
				continue
			}

			var valGraphql graphql.Output

			gt, ok := typeMap[valType.Kind()]
			if ok { // of scalars
				valGraphql = gt
			} else {
				if valType.Kind() == reflect.Ptr { // of pointers
					valType = valType.Elem()
				}

				// TODO: extract helper
				// Header map[string][]string
				if valType.Kind() == reflect.Slice {
					gt, ok := typeMap[valType.Elem().Kind()]
					if ok {
						valGraphql = graphql.NewList(graphql.NewNonNull(gt))
					}
				} else {
					valGraphql = reflectTransform(valType, registry) // of structs
				}
			}

			itemType := graphql.NewObject(
				graphql.ObjectConfig{
					Name: objectType.Name() + currentField.Name + "MapItem",
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

			fields[currentField.Name] = &graphql.Field{
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
				fields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(graphql.Int),
					Resolve: func(p graphql.ResolveParams) (interface{}, error) {
						parent := reflect.ValueOf(p.Source).Elem()
						val := parent.FieldByName(currentField.Name).Interface().(time.Time)
						return val.Unix(), nil
					},
				}
			} else { // user-defined struct
				fields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(reflectTransform(currentField.Type, registry)),
				}
			}

			continue
		}

		// pointer to struct
		if currentField.Type.Kind() == reflect.Ptr && currentField.Type.Elem().Kind() == reflect.Struct {
			fields[currentField.Name] = &graphql.Field{
				Name: currentField.Name,
				Type: reflectTransform(currentField.Type.Elem(), registry),
			}

			continue
		}

		// slice
		if currentField.Type.Kind() == reflect.Slice {
			if currentField.Type.Elem().Kind() == reflect.Struct { // of struct
				fields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(reflectTransform(currentField.Type.Elem(), registry)))),
				}
			} else if currentField.Type.Elem().Kind() == reflect.Ptr { // of pointer
				fields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphql.NewNonNull(graphql.NewList(reflectTransform(currentField.Type.Elem().Elem(), registry))),
				}
			} else { // of scalar type
				gt, ok := typeMap[currentField.Type.Elem().Kind()]
				if ok {
					fields[currentField.Name] = &graphql.Field{
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
				fields[currentField.Name] = &graphql.Field{
					Name: currentField.Name,
					Type: graphqlType,
				}
				continue
			}
		}

		graphqlType, ok := typeMap[currentField.Type.Kind()]
		if ok {
			fields[currentField.Name] = &graphql.Field{
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
			Fields: fields,
		},
	)

	(*registry)[objectType.Name()] = graphqlType

	return graphqlType
}

func buildSchema(client *api.Client) (graphql.Schema, error) {
	graphqlRegistry := map[string]graphql.Output{}
	allocationListStubType := reflectTransform(reflect.TypeOf(api.AllocationListStub{}), &graphqlRegistry)
	allocationType := reflectTransform(reflect.TypeOf(api.Allocation{}), &graphqlRegistry)

	fields := graphql.Fields{
		"allocations": &graphql.Field{
			Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(allocationListStubType))),
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
		"allocation": &graphql.Field{
			Type: allocationType,
			Args: graphql.FieldConfigArgument{
				"id": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				alloc, _, err := client.Allocations().Info(p.Args["id"].(string), nil)
				return alloc, err
			},
		},
	}

	rootQuery := graphql.ObjectConfig{Name: "Query", Fields: fields}
	schemaConfig := graphql.SchemaConfig{Query: graphql.NewObject(rootQuery)}
	return graphql.NewSchema(schemaConfig)
}
