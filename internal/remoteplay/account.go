package remoteplay

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// AccountIDFromUserID turns the decimal PSN user_id into the account ID the
// registration wants: the number as 8 little-endian bytes, base64.
func AccountIDFromUserID(userID string) (string, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(userID), 10, 64)
	if err != nil {
		return "", fmt.Errorf("remoteplay: user ID %q is not a number", userID)
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// NormalizeAccountID accepts the account ID in either form people find it
// in: the base64 of 8 bytes the registration sends, or the decimal PSN user
// ID. Base64 of 8 bytes always ends in "=", so the two never overlap.
func NormalizeAccountID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("remoteplay: account ID is empty")
	}
	if strings.Trim(s, "0123456789") == "" {
		return AccountIDFromUserID(s)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 8 {
		return "", errors.New("remoteplay: account ID must be the base64 account ID (12 characters) or the numeric PSN user ID")
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
