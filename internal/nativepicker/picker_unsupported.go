//go:build !darwin || !cgo

package nativepicker

import "errors"

// Available reports whether the platform has a native mixed-selection chooser.
func Available() bool { return false }

func Pick() ([]string, error) {
	return nil, errors.New("当前平台不支持原生文件与文件夹混选窗口")
}
