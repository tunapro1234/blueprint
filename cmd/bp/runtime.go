package main

import "blueprint/internal/buildinfo"

type runtimeProducer = buildinfo.Identity

func currentProducer() runtimeProducer { return buildinfo.Current() }
