//go:build !darwin && !windows

package main

func activateApp()       {}
func hideApp()           {}
func appFrontmost() bool { return false }
