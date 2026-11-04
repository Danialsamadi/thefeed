package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// The signing key and the import URI live in serverconfig, which the admin
// panel shares; these names stay for the server's callers.
const serverKeyFile = serverconfig.ServerKeyFile

var (
	LoadOrCreateServerKey = serverconfig.LoadOrCreateServerKey
	ServerPublicKeyString = serverconfig.ServerPublicKeyString
	ConfigURI             = serverconfig.ConfigURI
	decodeSeed            = serverconfig.DecodeSeed
)

// serverEncKeyFile holds the x25519 chat encryption private key (base64 std
// of the 32-byte scalar). Clients learn the matching public key from the
// signed ChatInfo payload, not from the import URI.
const serverEncKeyFile = "server_x25519.key"

// LoadOrCreateServerEncKey returns the server's x25519 encryption key,
// reading it from <dataDir>/server_x25519.key or generating and persisting a
// new one if the file is absent.
func LoadOrCreateServerEncKey(dataDir string) (*ecdh.PrivateKey, error) {
	path := filepath.Join(dataDir, serverEncKeyFile)

	if raw, err := os.ReadFile(path); err == nil {
		seed, derr := decodeSeed(raw)
		if derr != nil {
			return nil, fmt.Errorf("server enc key %s: %w", path, derr)
		}
		return ecdh.X25519().NewPrivateKey(seed)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read server enc key %s: %w", path, err)
	}

	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate server enc key: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	enc := base64.StdEncoding.EncodeToString(key.Bytes())
	if err := os.WriteFile(path, []byte(enc+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write server enc key %s: %w", path, err)
	}
	return key, nil
}

// SaveServerEncKey overwrites the persisted x25519 chat key. Used on ek
// rotation so a restart loads the rotated key, not the pre-rotation one. The
// previous key lives on only in RAM (its grace window), which is what gives the
// session layer forward secrecy once it expires.
func SaveServerEncKey(dataDir string, key *ecdh.PrivateKey) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, serverEncKeyFile)
	enc := base64.StdEncoding.EncodeToString(key.Bytes())
	return os.WriteFile(path, []byte(enc+"\n"), 0o600)
}
