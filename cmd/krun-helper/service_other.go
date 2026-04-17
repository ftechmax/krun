//go:build !linux && !windows

package main

func shouldRunAsService(_ bool) bool {
	return false
}

func runAsService(_, _ string) error {
	return nil
}
