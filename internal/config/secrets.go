package config

import (
	"context"
	"errors"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type SecretSlot string

const (
	SecretIdentityPrivateKey SecretSlot = "identity.private_key"
	SecretRelayCredential    SecretSlot = "relay.credential"
	SecretProxyCredential    SecretSlot = "relay.proxy_credential"
)

type SecretState string

const (
	SecretUnset       SecretState = "unset"
	SecretAvailable   SecretState = "available"
	SecretMissing     SecretState = "missing"
	SecretUnavailable SecretState = "unavailable"
)

type SecretReport map[SecretSlot]SecretState

func CheckSecretRefs(ctx context.Context, value Config, store platformpkg.SecureStore) (SecretReport, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := Validate(value); err != nil {
		return nil, err
	}
	references := []struct {
		slot SecretSlot
		ref  platformpkg.SecretRef
	}{
		{SecretIdentityPrivateKey, value.Identity.PrivateKeyRef},
		{SecretRelayCredential, value.Relay.CredentialRef},
		{SecretProxyCredential, value.Relay.ProxyCredentialRef},
	}
	report := make(SecretReport, len(references))
	for _, reference := range references {
		report[reference.slot] = SecretUnset
	}
	if store == nil || !store.Available() {
		for _, reference := range references {
			if reference.ref != "" {
				report[reference.slot] = SecretUnavailable
			}
		}
		return report, nil
	}
	for _, reference := range references {
		if reference.ref == "" {
			continue
		}
		secret, err := store.Get(ctx, reference.ref)
		clear(secret)
		switch {
		case err == nil:
			report[reference.slot] = SecretAvailable
		case errors.Is(err, platformpkg.ErrNotFound):
			report[reference.slot] = SecretMissing
		case errors.Is(err, platformpkg.ErrUnavailable):
			report[reference.slot] = SecretUnavailable
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		default:
			return nil, configError("secret check", ErrSecretCheck)
		}
	}
	return report, nil
}
