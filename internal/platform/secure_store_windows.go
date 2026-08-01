//go:build windows

package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	secureStoreMagic   = "YSS1"
	maxSecretBytes     = 1 << 20
	secureStoreFileExt = ".bin"
)

var secureStoreEntropy = []byte("yuanshu-secure-store-v1")

type dpapiSecureStore struct {
	root func() (string, error)
}

var _ SecureStore = (*dpapiSecureStore)(nil)

func newDPAPISecureStore(root func() (string, error)) *dpapiSecureStore {
	return &dpapiSecureStore{root: root}
}

func (*dpapiSecureStore) Available() bool { return true }

func (s *dpapiSecureStore) Put(ctx context.Context, ref SecretRef, secret []byte) error {
	if err := validateSecretCall(ctx, ref); err != nil {
		return err
	}
	if len(secret) > maxSecretBytes {
		return ErrInvalidArgument
	}

	ciphertext, err := protectCurrentUser(secret)
	if err != nil {
		return errors.New("secure store protection failed")
	}
	defer clear(ciphertext)

	root, path, err := s.resolve(ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return errors.New("secure store directory unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	temp, err := os.CreateTemp(root, ".secret-*")
	if err != nil {
		return errors.New("secure store write failed")
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		return errors.New("secure store write failed")
	}
	if _, err := temp.Write(append([]byte(secureStoreMagic), ciphertext...)); err != nil {
		return errors.New("secure store write failed")
	}
	if err := temp.Sync(); err != nil {
		return errors.New("secure store write failed")
	}
	if err := temp.Close(); err != nil {
		return errors.New("secure store write failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windows.Rename(tempPath, path); err != nil {
		return errors.New("secure store commit failed")
	}
	committed = true
	return nil
}

func (s *dpapiSecureStore) Get(ctx context.Context, ref SecretRef) ([]byte, error) {
	if err := validateSecretCall(ctx, ref); err != nil {
		return nil, err
	}
	_, path, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, errors.New("secure store read failed")
	}
	defer clear(raw)
	if len(raw) < len(secureStoreMagic) || !bytes.Equal(raw[:len(secureStoreMagic)], []byte(secureStoreMagic)) {
		return nil, errors.New("secure store data is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	secret, err := unprotectCurrentUser(raw[len(secureStoreMagic):])
	if err != nil {
		return nil, errors.New("secure store data is invalid")
	}
	return secret, nil
}

func (s *dpapiSecureStore) Delete(ctx context.Context, ref SecretRef) error {
	if err := validateSecretCall(ctx, ref); err != nil {
		return err
	}
	_, path, err := s.resolve(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return errors.New("secure store delete failed")
	}
	return nil
}

func (s *dpapiSecureStore) resolve(ref SecretRef) (string, string, error) {
	if s == nil || s.root == nil {
		return "", "", ErrUnavailable
	}
	root, err := s.root()
	if err != nil || root == "" || !filepath.IsAbs(root) {
		return "", "", ErrUnavailable
	}
	digest := sha256.Sum256([]byte(ref))
	return root, filepath.Join(root, hex.EncodeToString(digest[:])+secureStoreFileExt), nil
}

func validateSecretCall(ctx context.Context, ref SecretRef) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	text := string(ref)
	if text == "" || len(text) > 512 || strings.IndexFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ErrInvalidArgument
	}
	return nil
}

func protectCurrentUser(secret []byte) ([]byte, error) {
	in := dataBlob(secret)
	entropy := dataBlob(secureStoreEntropy)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}

func unprotectCurrentUser(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("empty ciphertext")
	}
	in := dataBlob(ciphertext)
	entropy := dataBlob(secureStoreEntropy)
	var out windows.DataBlob
	var description *uint16
	if err := windows.CryptUnprotectData(&in, &description, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	if description != nil {
		defer windows.LocalFree(windows.Handle(unsafe.Pointer(description)))
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := unsafe.Slice(out.Data, int(out.Size))
	copyOfPlain := append([]byte(nil), plain...)
	clear(plain)
	return copyOfPlain, nil
}

func dataBlob(data []byte) windows.DataBlob {
	if len(data) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}
