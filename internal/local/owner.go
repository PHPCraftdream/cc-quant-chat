package local

import "path/filepath"

// OwnerKeyPath is where the owner's key file lives: <CCQC_HOME>/owner.key.
func OwnerKeyPath() string { return filepath.Join(home(), "owner.key") }

// NodeKeyPath is where the broker's node key lives: <CCQC_HOME>/broker.key.
func NodeKeyPath() string { return filepath.Join(home(), "broker.key") }
