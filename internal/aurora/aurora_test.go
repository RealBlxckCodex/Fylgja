package aurora

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			w.Write([]byte(`{"version":"1.0.0","models":[{"id":"kokoro-v1","backend":"onnx","loaded":true}]}`))
		case "/v1/models":
			w.Write([]byte(`{"models":[{"id":"kokoro-v1","type":"tts","backend":"onnx","loaded":true},{"id":"whisper-turbo","type":"stt","backend":"whisper","loaded":true}]}`))
		default:
			w.WriteHeader(404) // ältere Version ohne /v1/languages
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/", APIKey: "k"}
	in := c.Info(context.Background())
	if !in.OK || in.Version != "1.0.0" || len(in.Models) != 2 || in.Models[1].Type != "stt" || in.Languages == nil {
		t.Fatalf("%+v", in)
	}
	bad := (&Client{BaseURL: srv.URL, APIKey: "falsch"}).Info(context.Background())
	if bad.OK || bad.Error == "" {
		t.Fatalf("falscher key: %+v", bad)
	}
	none := (*Client)(nil).Info(context.Background())
	if none.OK || none.Error == "" {
		t.Fatalf("nil: %+v", none)
	}
	down := (&Client{BaseURL: "http://127.0.0.1:1"}).Info(context.Background())
	if down.OK {
		t.Fatal("nicht erreichbar gilt als ok")
	}
}
