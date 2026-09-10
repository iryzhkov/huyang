//go:build !linux

package embed

type rootLock struct{}

func acquireRootLock(string) (*rootLock, error) { return &rootLock{}, nil }
func (l *rootLock) release()                    {}
func FindForeign(string) (string, int)          { return "", 0 }
