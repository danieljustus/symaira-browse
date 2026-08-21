package render

import (
	"encoding/json"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
)

// JSON serialises the Document as pretty-printed JSON.
func JSON(doc *agentdom.Document) (string, error) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
