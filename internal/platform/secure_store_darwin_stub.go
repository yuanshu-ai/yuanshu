//go:build darwin && !cgo

package platform

import "context"

type unavailableDarwinKeychain struct{}

func newDarwinKeychain() SecureStore                                           { return unavailableDarwinKeychain{} }
func (unavailableDarwinKeychain) Available() bool                              { return false }
func (unavailableDarwinKeychain) Put(context.Context, SecretRef, []byte) error { return ErrUnavailable }
func (unavailableDarwinKeychain) Get(context.Context, SecretRef) ([]byte, error) {
	return nil, ErrUnavailable
}
func (unavailableDarwinKeychain) Delete(context.Context, SecretRef) error { return ErrUnavailable }
