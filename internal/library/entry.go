// Package library scans local directories for audio files, playlists, and CUE
// sheets presented as virtual album folders.
//
// It is filesystem-first: there is no library database or index. Every call
// reads the filesystem, and CUE metadata comes from the CUE sheet itself.
package library

import (
	"time"

	"player2/internal/meta"
)

// Kind identifies what an Entry represents.
type Kind int

// Entry kinds. Directories and CUE folders are containers; audio, CUE tracks,
// and playlists are playable or loadable entries.
const (
	KindDir Kind = iota
	KindAudio
	KindCueDir
	KindCueTrack
	KindPlaylist
	KindInvalidCue
)

// Entry is one item produced by the scanner.
//
// Path is always an absolute, cleaned path. For KindCueTrack it is the audio
// file containing the track and CuePath identifies the sheet. Start and
// Duration are set for CUE tracks only. Err is set for KindInvalidCue.
type Entry struct {
	Kind     Kind
	Name     string
	Path     string // real path
	CuePath  string
	TrackNo  int
	Start    time.Duration
	Duration time.Duration
	Meta     meta.Info
	Err      error
}

// Options controls scanner behavior.
type Options struct {
	ShowHidden          bool
	FollowSymlinks      bool
	CueFallbackEncoding string
	AudioExtensions     []string
}
