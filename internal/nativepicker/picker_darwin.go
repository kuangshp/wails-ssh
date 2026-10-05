//go:build darwin && cgo

// Package nativepicker provides the native file-and-directory upload chooser.
package nativepicker

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "picker_darwin.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

var pickerMu sync.Mutex

func Available() bool { return true }

// Pick opens an asynchronous, window-modal macOS sheet. The calling Wails RPC
// goroutine waits for its result, but the Cocoa main thread remains responsive.
// Callers must not invoke Pick from a Cocoa main-thread callback.
func Pick() ([]string, error) {
	if !pickerMu.TryLock() {
		return nil, errors.New("文件选择窗口已打开")
	}
	defer pickerMu.Unlock()

	result := C.ssh_pick_upload_paths()
	defer C.free(unsafe.Pointer(result.paths_json))
	defer C.free(unsafe.Pointer(result.error_message))
	if result.error_message != nil {
		return nil, errors.New(C.GoString(result.error_message))
	}
	if result.paths_json == nil {
		return nil, errors.New("无法读取文件选择结果")
	}
	paths := []string{}
	if err := json.Unmarshal([]byte(C.GoString(result.paths_json)), &paths); err != nil {
		return nil, fmt.Errorf("无法读取文件选择结果: %w", err)
	}
	return paths, nil
}
