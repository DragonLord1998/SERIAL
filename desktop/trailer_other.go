//go:build !darwin || !cgo

package main

import "errors"

type TrailerPreviewStatus struct {
	Position float64 `json:"position"`
	Token    uint64  `json:"token"`
	Phase    string  `json:"phase"`
	Message  string  `json:"message"`
}

func startTrailerPreview(youtubeID string, x, y, width, height float64, token uint64) error {
	return errors.New("Native trailer previews require macOS")
}
func moveTrailerPreview(token uint64, x, y, width, height float64) error {
	return errors.New("Native trailer previews require macOS")
}
func pauseTrailerPreview(token uint64) error {
	return errors.New("Native trailer previews require macOS")
}
func resumeTrailerPreview(token uint64) error {
	return errors.New("Native trailer previews require macOS")
}
func restartTrailerPreview(token uint64) error {
	return errors.New("Native trailer previews require macOS")
}
func stopTrailerPreview(token uint64) {}
func trailerPreviewStatus(token uint64) TrailerPreviewStatus {
	return TrailerPreviewStatus{Token: token, Phase: "unavailable", Message: "Native trailer previews require macOS"}
}
