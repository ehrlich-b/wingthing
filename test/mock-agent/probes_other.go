//go:build !linux

package main

func probeSeccomp() SeccompProbe {
	return SeccompProbe{}
}

func probeNamespace() NamespaceProbe {
	return NamespaceProbe{}
}
