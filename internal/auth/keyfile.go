package auth

// LoadOrCreateKey returns the persistent session signing key stored at path.
func LoadOrCreateKey(path string) (key []byte, regenerated bool, err error) {
	return nil, false, errNotImplemented
}
