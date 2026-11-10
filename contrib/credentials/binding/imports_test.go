package binding

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImports keeps binding free of the SDK, so Temporal Proxy can import it
// without compiling the SDK.
func TestImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-json", ".").Output()
	require.NoError(t, err)
	var pkg struct {
		Imports []string
		Deps    []string
	}
	require.NoError(t, json.Unmarshal(out, &pkg))
	allowed := []string{"go.temporal.io/api/", "google.golang.org/grpc", "google.golang.org/protobuf/"}
	for _, imp := range pkg.Imports {
		if !strings.Contains(imp, ".") {
			continue // standard library
		}
		ok := false
		for _, prefix := range allowed {
			ok = ok || strings.HasPrefix(imp, prefix)
		}
		require.True(t, ok, "binding imports %s; it may import only go.temporal.io/api, gRPC, protobuf, and the standard library", imp)
	}
	for _, dep := range pkg.Deps {
		require.False(t, strings.HasPrefix(dep, "go.temporal.io/sdk"), "binding depends on %s", dep)
	}
}
