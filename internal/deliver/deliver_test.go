package deliver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// N7: 对方回复里的控制字符(比如终端转义序列)不进入失败原因。
func TestN7ErrorStripsControlCharacters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("bad\x1b[2J\x1b]0;title\x07 day\n"))
	}))
	defer srv.Close()
	err := Send(context.Background(), srv.Client(), srv.URL, "r/1", Payload{})
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07\n") || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err=%q", err)
	}
}
