//go:build !windows

package local

import "errors"

type dpapiStore struct{}

func newDPAPIStore(string) secretStore { return dpapiStore{} }

func (dpapiStore) get() ([]byte, error) { return nil, errors.New("DPAPI exists on Windows only") }
func (dpapiStore) put([]byte) error     { return errors.New("DPAPI exists on Windows only") }
