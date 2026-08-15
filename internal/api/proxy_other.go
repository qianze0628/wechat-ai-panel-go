//go:build !windows

package api

import "net/http"

func init() {
	systemProxyFor = func(req *http.Request) string { return "" }
}
