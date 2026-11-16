// Package webwire validates the generated builtin-web-retrieval/1 bundle.
package webwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/google/uuid"
)

func Validate(name string, raw []byte) error {
	if wire.Validate(name, raw) != nil {
		return d.Invalid
	}
	return nil
}
func Request(raw []byte) (d.Request, error) {
	if err := Validate("OperationRequest", raw); err != nil {
		return d.Request{}, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return d.Request{}, d.Invalid
	}
	var id, tool string
	if json.Unmarshal(fields["operation_id"], &id) != nil || json.Unmarshal(fields["tool"], &tool) != nil {
		return d.Request{}, d.Invalid
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return d.Request{}, d.Invalid
	}
	var args any
	dec := json.NewDecoder(bytes.NewReader(fields["arguments"]))
	dec.UseNumber()
	if dec.Decode(&args) != nil {
		return d.Request{}, d.Invalid
	}
	canonical, err := json.Marshal(args)
	if err != nil {
		return d.Request{}, d.Invalid
	}
	// Identity is tool plus canonical JSON arguments; never log this material.
	sum := sha256.Sum256(append(append([]byte(tool), 0), canonical...))
	return d.Request{ID: parsed, Tool: tool, Arguments: canonical, Digest: hex.EncodeToString(sum[:])}, nil
}
