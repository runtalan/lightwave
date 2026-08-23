//go:build !darwin

package main

func activateApp()       {}
func hideApp()           {}
func appFrontmost() bool { return false }
