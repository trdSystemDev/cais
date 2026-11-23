//go:build !unix

package main

func stderrIsJournal() bool { return false }
