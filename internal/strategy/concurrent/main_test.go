// lint:ignore-leak-check
package concurrent

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/SurgeDM/Surge/internal/transport"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	code := m.Run()
	http.DefaultClient.CloseIdleConnections()
	transport.DefaultNetworkPool.CloseAll()
	if code == 0 {
		if err := goleak.Find(
			goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
			goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: Errors on successful test run: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(code)
}
