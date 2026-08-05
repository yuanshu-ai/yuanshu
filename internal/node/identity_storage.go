package node

import (
	"errors"
	"path/filepath"

	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/identity"
)

var errIdentityRepairRequired = errors.New("node identity requires explicit repair")

func newNodeIdentityStore(root string, value config.IdentityConfig) (*identity.FileKeyStore, error) {
	if value.PrivateKeyRef != "" {
		return nil, errIdentityRepairRequired
	}
	keyFile := value.KeyFile
	if keyFile == "" {
		keyFile = config.DefaultIdentityKeyFile
	}
	return identity.NewFileKeyStore(filepath.Join(root, keyFile))
}
