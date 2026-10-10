package wing

import (
	"errors"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/auth"
)

// Older device credentials lacked their authenticated user ID. Resolve that
// binding once from their issuing roost and persist it for offline wing starts.
func bindLocalWingOwner(dir, roost string, token *auth.DeviceToken, local bool) (string, error) {
	if token.UserID != "" {
		return token.UserID, nil
	}
	info, err := auth.FetchUserInfo(roost, token.Token)
	if err != nil {
		return "", fmt.Errorf("upgrade the wing owner's binding while its roost is reachable: %w", err)
	}
	if info.UserID == "" {
		return "", errors.New("roost did not identify the wing owner; upgrade the roost")
	}
	store := auth.NewTokenStore(dir)
	if local {
		localStore := auth.NewLocalTokenStore(dir)
		selected, err := localStore.Load()
		if err != nil {
			return "", err
		}
		if selected != nil && selected.Token == token.Token {
			store = localStore
		}
	}
	selected, err := store.Load()
	if err != nil {
		return "", err
	}
	if selected == nil || selected.Token != token.Token {
		return "", errors.New("wing credential changed while resolving its owner; restart the wing")
	}
	selected.UserID = info.UserID
	if err := store.Save(selected); err != nil {
		return "", err
	}
	token.UserID = info.UserID
	return info.UserID, nil
}
