// Package pigo is a vendored copy of the PICO object-detection cascade from
// github.com/esimov/pigo, v1.4.6, MIT licensed. See LICENSE in this directory
// for the original copyright notice, which travels with the code.
//
// WHY IT IS HERE RATHER THAN IN go.mod. This tree builds in GOPATH mode as well
// as module mode, and in GOPATH mode go.mod is ignored entirely: an external
// import has to exist as source under $GOPATH/src on every machine that builds.
// That is a setup step the repository cannot carry, so a dependency declared
// only in go.mod builds here and fails on a fresh checkout — which is exactly
// what happened when this was first added. Vendoring makes both modes work with
// no per-machine setup.
//
// WHAT WAS TAKEN. pigo.go, grayscale.go and utils.go, byte for byte, so that a
// diff against upstream is meaningful and a future version bump is a copy
// rather than a merge. Nothing here has been edited; if it ever needs to be,
// say so at the point of the change and keep it minimal.
//
// WHAT WAS LEFT. flploc.go (facial landmarks), puploc.go (pupil localization),
// image.go (file IO helpers) and doc.go. The face pass needs a box around a
// face and nothing finer, and the omitted files carry their own cascade formats
// and asset loading.
//
// WHAT IT IS FOR. Locating a face in a rendered image so the second pass of an
// edit can be spent on it — see core/image_face_detect.go. It answers "a face
// is here", never "this is a particular person": there is no recognition in
// this package and none is wanted, since identity in this codebase is keyed on
// a transport handle and not on pixels.
package pigo
