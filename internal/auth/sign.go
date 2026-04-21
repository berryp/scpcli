package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
)

const (
	ClientType = "OpenApi"
	Language   = "en-US"
)

// Sign computes the HMAC-SHA256 signature over:
//   method + encodedURL + timestamp + accessKey + projectID + clientType
//
// SCP uses JavaScript's encodeURI semantics; url.Parse + String() reproduces this.
func Sign(method, rawURL, accessKey, secretKey, projectID string, tsMillis int64) string {
	encoded := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		encoded = u.String()
	}
	msg := method + encoded + strconv.FormatInt(tsMillis, 10) + accessKey + projectID + ClientType
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(msg))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
