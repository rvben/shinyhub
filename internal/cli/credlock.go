package cli

import (
	"os"
	"path/filepath"
)

// mutateStore serializes the entire read-modify-write transaction. The lock
// uses a sibling file because saveStore atomically replaces the credentials
// inode. Every production writer goes through this function.
func mutateStore(fn func(*credentialStore) (bool, error)) (*credentialStore, error) {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	var result *credentialStore
	err := withStoreLock(path+".lock", func() error {
		st, err := loadStore()
		if err != nil {
			return err
		}
		changed, err := fn(st)
		if err != nil {
			return err
		}
		if changed {
			if len(st.Hosts) == 0 {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			} else if err := saveStore(st); err != nil {
				return err
			}
		}
		result = st
		return nil
	})
	return result, err
}
