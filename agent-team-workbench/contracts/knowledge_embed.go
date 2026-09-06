package contracts

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed control/knowledge-librarian-v1.schema.json
var knowledgeLibrarianSchema []byte

func KnowledgeLibrarianSchema() []byte { return slices.Clone(knowledgeLibrarianSchema) }
func KnowledgeLibrarianSchemaDigest() string {
	sum := sha256.Sum256(knowledgeLibrarianSchema)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var compiledKnowledgeSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(knowledgeLibrarianSchema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const resource = "https://workbench.local/contracts/knowledge-librarian/v1"
	if err = compiler.AddResource(resource, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(resource)
})

func ValidateKnowledgeLibrarianJSON(raw []byte) error {
	schema, err := compiledKnowledgeSchema()
	if err != nil {
		return fmt.Errorf("compile knowledge schema: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return schema.Validate(doc)
}
