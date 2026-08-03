//go:build darwin && cgo

package platform

/*
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static CFDictionaryRef yuanshu_keychain_query(CFStringRef account) {
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount };
	const void *values[] = { kSecClassGenericPassword, CFSTR("com.yuanshu.node.v1"), account };
	return CFDictionaryCreate(kCFAllocatorDefault, keys, values, 3,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
}

static int yuanshu_keychain_put(const char *account, const unsigned char *bytes, size_t length) {
	CFStringRef account_ref = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (account_ref == NULL) return errSecParam;
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, bytes, (CFIndex)length);
	if (data == NULL) {
		CFRelease(account_ref);
		return errSecAllocate;
	}
	CFDictionaryRef query = yuanshu_keychain_query(account_ref);
	if (query == NULL) {
		CFRelease(data);
		CFRelease(account_ref);
		return errSecAllocate;
	}
	const void *update_keys[] = { kSecValueData };
	const void *update_values[] = { data };
	CFDictionaryRef update = CFDictionaryCreate(kCFAllocatorDefault, update_keys, update_values, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (update == NULL) {
		CFRelease(query);
		CFRelease(data);
		CFRelease(account_ref);
		return errSecAllocate;
	}
	OSStatus status = SecItemUpdate(query, update);
	if (status == errSecItemNotFound) {
		const void *add_keys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecValueData };
		const void *add_values[] = { kSecClassGenericPassword, CFSTR("com.yuanshu.node.v1"), account_ref, data };
		CFDictionaryRef add = CFDictionaryCreate(kCFAllocatorDefault, add_keys, add_values, 4,
			&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
		if (add == NULL) {
			status = errSecAllocate;
		} else {
			status = SecItemAdd(add, NULL);
			CFRelease(add);
		}
	}
	CFRelease(update);
	CFRelease(query);
	CFRelease(data);
	CFRelease(account_ref);
	return (int)status;
}

static int yuanshu_keychain_get(const char *account, unsigned char **bytes, size_t *length) {
	*bytes = NULL;
	*length = 0;
	CFStringRef account_ref = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (account_ref == NULL) return errSecParam;
	CFDictionaryRef base = yuanshu_keychain_query(account_ref);
	if (base == NULL) {
		CFRelease(account_ref);
		return errSecAllocate;
	}
	const void *keys[] = { kSecReturnData, kSecMatchLimit };
	const void *values[] = { kCFBooleanTrue, kSecMatchLimitOne };
	CFMutableDictionaryRef query = CFDictionaryCreateMutableCopy(kCFAllocatorDefault, 0, base);
	if (query == NULL) {
		CFRelease(base);
		CFRelease(account_ref);
		return errSecAllocate;
	}
	CFDictionarySetValue(query, keys[0], values[0]);
	CFDictionarySetValue(query, keys[1], values[1]);
	CFTypeRef result = NULL;
	OSStatus status = SecItemCopyMatching(query, &result);
	if (status == errSecSuccess && result != NULL && CFGetTypeID(result) == CFDataGetTypeID()) {
		CFIndex size = CFDataGetLength((CFDataRef)result);
		if (size > 0) {
			*bytes = malloc((size_t)size);
			if (*bytes == NULL) status = errSecAllocate;
			else {
				memcpy(*bytes, CFDataGetBytePtr((CFDataRef)result), (size_t)size);
				*length = (size_t)size;
			}
		}
	}
	if (result != NULL) CFRelease(result);
	CFRelease(query);
	CFRelease(base);
	CFRelease(account_ref);
	return (int)status;
}

static int yuanshu_keychain_delete(const char *account) {
	CFStringRef account_ref = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (account_ref == NULL) return errSecParam;
	CFDictionaryRef query = yuanshu_keychain_query(account_ref);
	if (query == NULL) {
		CFRelease(account_ref);
		return errSecAllocate;
	}
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	CFRelease(account_ref);
	return (int)status;
}

static int yuanshu_keychain_not_found(void) { return errSecItemNotFound; }
*/
import "C"

import (
	"context"
	"errors"
	"strings"
	"unsafe"
)

type darwinKeychain struct{}

func newDarwinKeychain() SecureStore { return darwinKeychain{} }

func (darwinKeychain) Available() bool { return true }

func (darwinKeychain) Put(ctx context.Context, ref SecretRef, secret []byte) error {
	if err := validateDarwinSecretCall(ctx, ref); err != nil {
		return err
	}
	account := C.CString(string(ref))
	defer C.free(unsafe.Pointer(account))
	var data *C.uchar
	if len(secret) > 0 {
		data = (*C.uchar)(C.CBytes(secret))
		defer func() {
			C.memset(unsafe.Pointer(data), 0, C.size_t(len(secret)))
			C.free(unsafe.Pointer(data))
		}()
	}
	status := C.yuanshu_keychain_put(account, data, C.size_t(len(secret)))
	return darwinKeychainError(int(status))
}

func (darwinKeychain) Get(ctx context.Context, ref SecretRef) ([]byte, error) {
	if err := validateDarwinSecretCall(ctx, ref); err != nil {
		return nil, err
	}
	account := C.CString(string(ref))
	defer C.free(unsafe.Pointer(account))
	var data *C.uchar
	var length C.size_t
	status := C.yuanshu_keychain_get(account, &data, &length)
	if data != nil {
		defer func() {
			C.memset(unsafe.Pointer(data), 0, length)
			C.free(unsafe.Pointer(data))
		}()
	}
	if err := darwinKeychainError(int(status)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if uint64(length) > uint64(^uint32(0)>>1) {
		return nil, errors.New("macOS Keychain secret is too large")
	}
	return C.GoBytes(unsafe.Pointer(data), C.int(length)), nil
}

func (darwinKeychain) Delete(ctx context.Context, ref SecretRef) error {
	if err := validateDarwinSecretCall(ctx, ref); err != nil {
		return err
	}
	account := C.CString(string(ref))
	defer C.free(unsafe.Pointer(account))
	status := C.yuanshu_keychain_delete(account)
	return darwinKeychainError(int(status))
}

func validateDarwinSecretCall(ctx context.Context, ref SecretRef) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	value := string(ref)
	if value == "" || len(value) > 512 || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ErrInvalidArgument
	}
	return nil
}

func darwinKeychainError(status int) error {
	switch {
	case status == 0:
		return nil
	case status == int(C.yuanshu_keychain_not_found()):
		return ErrNotFound
	default:
		return errors.New("macOS Keychain operation failed")
	}
}
