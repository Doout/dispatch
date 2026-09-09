//go:build !linux

package installation

import "errors"

func lockDirectory(string) (func(), error) {
	return nil, errors.New("managed installations currently require Linux")
}
func socketGroup(string) (uint32, error) {
	return 0, errors.New("managed installations currently require Linux")
}
