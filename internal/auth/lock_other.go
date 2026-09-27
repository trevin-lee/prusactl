//go:build !unix

package auth

func lockRefresh() (unlock func(), err error) { return func() {}, nil }
