//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "trailer_darwin.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"unsafe"
)

type TrailerPreviewStatus struct {
	Position float64 `json:"position"`
	Token    uint64  `json:"token"`
	Phase    string  `json:"phase"`
	Message  string  `json:"message"`
}

var trailerYouTubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func startTrailerPreview(youtubeID string, x, y, width, height float64, token uint64) error {
	if !trailerYouTubeID.MatchString(youtubeID) || token == 0 || token > 9007199254740991 {
		return errors.New("invalid trailer identity or generation")
	}
	for _, n := range []float64{x, y, width, height} {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return errors.New("invalid trailer bounds")
		}
	}
	if x < 0 || y < 0 || width < 200 || height < 200 || width > 8192 || height > 8192 {
		return errors.New("trailer viewport must be visible and at least 200 by 200 points")
	}
	id := C.CString(youtubeID)
	defer C.free(unsafe.Pointer(id))
	result := C.goanime_trailer_start(id, C.double(x), C.double(y), C.double(width), C.double(height), C.uint64_t(token))
	if result != nil {
		defer C.free(unsafe.Pointer(result))
		return errors.New(C.GoString(result))
	}
	return nil
}
func moveTrailerPreview(token uint64, x, y, width, height float64) error {
	value := C.goanime_trailer_move(C.uint64_t(token), C.double(x), C.double(y), C.double(width), C.double(height))
	if value != nil {
		defer C.free(unsafe.Pointer(value))
		return errors.New(C.GoString(value))
	}
	return nil
}
func trailerPreviewCommand(token uint64, command string) error {
	action := C.CString(command)
	defer C.free(unsafe.Pointer(action))
	result := C.goanime_trailer_command(C.uint64_t(token), action)
	if result != nil {
		defer C.free(unsafe.Pointer(result))
		return errors.New(C.GoString(result))
	}
	return nil
}
func pauseTrailerPreview(token uint64) error   { return trailerPreviewCommand(token, "pause") }
func resumeTrailerPreview(token uint64) error  { return trailerPreviewCommand(token, "resume") }
func restartTrailerPreview(token uint64) error { return trailerPreviewCommand(token, "restart") }
func stopTrailerPreview(token uint64)          { C.goanime_trailer_stop(C.uint64_t(token)) }
func trailerPreviewStatus(token uint64) TrailerPreviewStatus {
	value := C.goanime_trailer_status(C.uint64_t(token))
	if value == nil {
		return TrailerPreviewStatus{Token: token, Phase: "unavailable", Message: "Native trailer status unavailable"}
	}
	defer C.free(unsafe.Pointer(value))
	var status TrailerPreviewStatus
	if err := json.Unmarshal([]byte(C.GoString(value)), &status); err != nil {
		return TrailerPreviewStatus{Token: token, Phase: "unavailable", Message: "Native trailer status unavailable"}
	}
	return status
}
