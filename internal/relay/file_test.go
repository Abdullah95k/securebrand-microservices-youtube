package relay_test

import "os"

// writeFile writes a credential file as Kubernetes mounts it: readable by
// its owner only.
func writeFile(path, v string) error { return os.WriteFile(path, []byte(v), 0o600) }
