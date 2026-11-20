//go:build !darwin && !windows

package main

func activateApp()       {}
func hideApp()           {}
func appFrontmost() bool { return false }

// watchActivation is nil here: there is no activation to watch.
func watchActivation() <-chan bool { return nil }
