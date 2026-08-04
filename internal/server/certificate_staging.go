package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type managedCertificateSwap struct {
	stagingRoot string
	stagedDir   string
	currentDir  string
	backupDir   string
	hadCurrent  bool
	installed   bool
	committed   bool
}

func stageManagedCertificate(ctx context.Context, value ConfigFile) (*managedCertificateSwap, error) {
	if value.DeploymentMode != DeploymentLANManaged {
		return nil, nil
	}
	stagingRoot, err := os.MkdirTemp(value.DataDir, ".pki-stage-")
	if err != nil || os.Chmod(stagingRoot, 0o700) != nil {
		return nil, errors.New("managed certificate staging is unavailable")
	}
	swap := &managedCertificateSwap{
		stagingRoot: stagingRoot,
		stagedDir:   filepath.Join(stagingRoot, "pki", "managed"),
		currentDir:  filepath.Join(value.DataDir, "pki", "managed"),
		backupDir:   filepath.Join(value.DataDir, "pki", ".managed-pre-"+filepath.Base(stagingRoot)),
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(stagingRoot)
		}
	}()
	if info, statErr := os.Lstat(swap.currentDir); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("managed certificate directory is unsafe")
		}
		swap.hadCurrent = true
		currentCert, currentKey := managedCAPaths(value.DataDir)
		if _, _, loadErr := loadManagedCA(currentCert, currentKey, time.Now()); loadErr != nil {
			return nil, errors.New("existing managed CA is invalid")
		}
		if preparePKIDirectory(swap.stagedDir) != nil || copyPrivatePKIFile(currentCert, filepath.Join(swap.stagedDir, "ca.pem")) != nil || copyPrivatePKIFile(currentKey, filepath.Join(swap.stagedDir, "ca-key.pem")) != nil {
			return nil, errors.New("managed CA could not be staged")
		}
		metadata := filepath.Join(swap.currentDir, "recovery-backup.json")
		if _, metadataErr := os.Lstat(metadata); metadataErr == nil {
			if copyPrivatePKIFile(metadata, filepath.Join(swap.stagedDir, "recovery-backup.json")) != nil {
				return nil, errors.New("managed CA backup metadata could not be staged")
			}
		} else if !errors.Is(metadataErr, os.ErrNotExist) {
			return nil, errors.New("managed CA backup metadata is unavailable")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, errors.New("managed certificate directory is unavailable")
	}
	staged := value
	staged.DataDir = stagingRoot
	provider, providerErr := newManagedCertificateProvider(ctx, optionsFromConfig(staged))
	if providerErr != nil {
		return nil, errors.New("managed certificate initialization failed")
	}
	_ = provider.Close()
	failed = false
	return swap, nil
}

func (s *managedCertificateSwap) install() error {
	if s == nil || s.installed {
		return ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(s.currentDir), 0o700); err != nil {
		return err
	}
	if s.hadCurrent {
		if err := os.Rename(s.currentDir, s.backupDir); err != nil {
			return err
		}
	}
	if err := os.Rename(s.stagedDir, s.currentDir); err != nil {
		if s.hadCurrent {
			_ = os.Rename(s.backupDir, s.currentDir)
		}
		return err
	}
	s.installed = true
	if err := syncServerDirectory(filepath.Dir(s.currentDir)); err != nil {
		if rollbackErr := s.rollback(); rollbackErr != nil {
			return rollbackErr
		}
		return err
	}
	return nil
}

func (s *managedCertificateSwap) rollback() error {
	if s == nil || !s.installed || s.committed {
		return nil
	}
	if err := os.RemoveAll(s.currentDir); err != nil {
		return err
	}
	if s.hadCurrent {
		if err := os.Rename(s.backupDir, s.currentDir); err != nil {
			return err
		}
	}
	s.installed = false
	return syncServerDirectory(filepath.Dir(s.currentDir))
}

func (s *managedCertificateSwap) commit() {
	if s == nil {
		return
	}
	s.committed = true
	if s.hadCurrent {
		_ = os.RemoveAll(s.backupDir)
		_ = syncServerDirectory(filepath.Dir(s.currentDir))
	}
}

func (s *managedCertificateSwap) cleanup() {
	if s != nil {
		_ = os.RemoveAll(s.stagingRoot)
	}
}

func copyPrivatePKIFile(source, target string) error {
	if err := validateManagedPKIFile(source); err != nil {
		return err
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return atomicWriteServerFile(target, raw)
}
