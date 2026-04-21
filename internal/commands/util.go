package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

func PrintJSON(w io.Writer, data []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		_, err = w.Write(data)
		return err
	}
	_, err := buf.WriteTo(w)
	return err
}

// resolveBody converts a --body flag value to bytes.
// Empty string → nil. "@path" → file contents. Otherwise the string itself.
func resolveBody(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "@") {
		return os.ReadFile(s[1:])
	}
	return []byte(s), nil
}
