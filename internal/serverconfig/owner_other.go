//go:build !unix

package serverconfig

// chownLike is a no-op where files have no Unix owner.
func chownLike(name, like string) error { return nil }
