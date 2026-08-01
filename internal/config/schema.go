package config

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const embeddedSchemaID = "https://yuanshu.ai/schemas/config/v1/node-config.schema.json"

//go:embed node-config.schema.json
var embeddedSchemaJSON string

var (
	compiledSchemaOnce sync.Once
	compiledSchema     *jsonschema.Schema
	compiledSchemaErr  error
)

func validateSchema(value any) error {
	compiledSchemaOnce.Do(func() {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(embeddedSchemaJSON))
		if err != nil {
			compiledSchemaErr = err
			return
		}
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		if err := compiler.AddResource(embeddedSchemaID, document); err != nil {
			compiledSchemaErr = err
			return
		}
		compiledSchema, compiledSchemaErr = compiler.Compile(embeddedSchemaID)
	})
	if compiledSchemaErr != nil {
		return configError("schema", ErrInvalid)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return configError("schema", ErrInvalid)
	}
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return configError("schema", ErrInvalid)
	}
	if err := compiledSchema.Validate(document); err != nil {
		return configError("schema", ErrInvalid)
	}
	return nil
}
