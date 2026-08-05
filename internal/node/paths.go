package node

import "path/filepath"

// pathsForConfig keeps an explicitly selected configuration isolated from the
// default Node data directory. This is important for local test profiles and
// for users who run more than one Node profile on the same account.
func pathsForConfig(defaults paths, configPath string) paths {
	configPath = filepath.Clean(configPath)
	if configPath == defaults.config {
		return defaults
	}
	root := filepath.Dir(configPath)
	return paths{
		root:     root,
		config:   configPath,
		database: filepath.Join(root, "node.db"),
		log:      filepath.Join(root, "node.log"),
	}
}
