// Package playback runs listening sessions: radio and fixed queues, listen
// events, taste persistence, and the mix shelf. Radio started from a track,
// artist, or album keeps that anchor in the taste blend until the mode
// changes; closer and wider are two proximity steps on the same session.
// Mixes in mixes.json are the only shelf registry; GeneratedMixContract is
// the list a mix_pack job sends the worker.
// HTTP lives in internal/api.
package playback
