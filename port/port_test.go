package port_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/imohiyoko/API/port"
)

func TestPrincipalHidesToken(t *testing.T) {
	p := port.Principal{Type: port.PrincipalUser, ID: "usr_1", Token: "secret-token"}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "principal", p)
	for _, s := range []string{fmt.Sprintf("%v %+v %s", p, p, p), buf.String()} {
		if strings.Contains(s, "secret-token") || !strings.Contains(s, "usr_1") {
			t.Errorf("got %q", s)
		}
	}
}
