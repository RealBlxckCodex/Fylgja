package policy

import (
	"bytes"
	"io"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// extractJSON schneidet ein JSON-Objekt aus Modellausgaben (z. B. ```json-Blöcke).
func extractJSON(b []byte) []byte {
	s := bytes.IndexByte(b, '{')
	e := bytes.LastIndexByte(b, '}')
	if s < 0 || e < s {
		return b
	}
	return b[s : e+1]
}
