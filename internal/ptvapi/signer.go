// Package ptvapi implements a signed client for the PTV Timetable API v3.
package ptvapi

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"strings"
)

// sign computes the PTV request signature.
//
// The signature is the uppercase hex HMAC-SHA1 of the request path plus query
// string (including the devid parameter, excluding the signature parameter),
// keyed by the API key. pathAndQuery must begin with "/v3/" and already
// contain the devid parameter.
func sign(apiKey, pathAndQuery string) string {
	mac := hmac.New(sha1.New, []byte(apiKey))
	mac.Write([]byte(pathAndQuery))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

// buildSignedURL assembles a fully signed absolute URL for the given API path
// and query values. path must start with "/v3/". The devid and signature
// parameters are appended automatically.
func buildSignedURL(baseURL, apiKey, devID, path string, query url.Values) string {
	if query == nil {
		query = url.Values{}
	}
	query.Set("devid", devID)

	pathAndQuery := path + "?" + query.Encode()
	signature := sign(apiKey, pathAndQuery)

	return strings.TrimRight(baseURL, "/") + pathAndQuery + "&signature=" + signature
}
