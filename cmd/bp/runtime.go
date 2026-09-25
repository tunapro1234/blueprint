package main

import "blueprint/internal/buildinfo"

type runtimeProducer = buildinfo.Identity

func currentProducer(cachePath ...string) runtimeProducer {
	if len(cachePath) > 0 {
		return buildinfo.CurrentCached(cachePath[0])
	}
	return buildinfo.Current()
}
