package volgate

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// jsonUnmarshalStrict decodes JSON and rejects unknown fields, so a typo in a
// config file is an error instead of a silently ignored setting.
func jsonUnmarshalStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}
