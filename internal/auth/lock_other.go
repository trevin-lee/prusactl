//go:build !unix && !windows

package auth

func lockRefresh() (unlock func(), err error) { return func() {}, nil }
