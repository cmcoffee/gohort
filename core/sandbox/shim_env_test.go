package sandbox

import (
	"strings"
	"testing"
)

// PATH inside a sandbox has to lead with the shim bin dir, or a script calling
// fetch_url as an ordinary command finds nothing. Which dir that is depends on
// whether the backend remaps paths: under a remapping backend it is the mount,
// otherwise the host dir the shims were written to.
func TestSandboxEnvPrependsShimBin(t *testing.T) {
	pathFor := func(remaps bool) string {
		t.Helper()
		for _, kv := range sandboxEnv(remaps) {
			if strings.HasPrefix(kv, "PATH=") {
				return kv[len("PATH="):]
			}
		}
		t.Fatal("sandboxEnv produced no PATH")
		return ""
	}

	if path := pathFor(true); !strings.HasPrefix(path, GohortBinMountPath+":") {
		t.Errorf("with remapping, PATH %q not prefixed with the shim mount %q", path, GohortBinMountPath)
	}

	path := pathFor(false)
	if strings.HasPrefix(path, GohortBinMountPath+":") {
		t.Errorf("without remapping, PATH leads with the unmounted %q: %q", GohortBinMountPath, path)
	}
	if want := sandboxShimBinDir(false); want != "" && !strings.HasPrefix(path, want+":") {
		t.Errorf("without remapping, PATH %q should lead with the host shim dir %q", path, want)
	}
}
