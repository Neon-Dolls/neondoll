//go:build !acceptance

package main

import body "github.com/Neon-Dolls/neondoll/Core/Body"

func startAcceptanceControl(*body.Registry) (func(), error) {
	return func() {}, nil
}
